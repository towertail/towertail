import Foundation

/// Runs the bundled sampler with `scan` as a child process. The stdin
/// pipe stays open for the whole scan: the sampler stops when stdin
/// closes. Cancel closes stdin, then terminates the process after a
/// grace period.
struct LocalDiskScanner: DiskScanner {
    /// nil uses the bundled sampler.
    var binary: URL?
    /// Arguments before the scan arguments. Tests use it to run a script.
    var leadingArguments: [String] = []
    var watchdog: TimeInterval = 60
    var cancelGrace: TimeInterval = 2

    func scan(node: Node, request: DiskScanRequest) -> AsyncThrowingStream<DiskScanEvent, Error> {
        let binary = self.binary ?? LocalSamplerInvoker.resolveBinary()
        let args = leadingArguments + request.samplerArguments
        let watchdog = self.watchdog
        let grace = self.cancelGrace
        return AsyncThrowingStream { continuation in
            guard let binary else {
                continuation.finish(throwing: DiskScanError.binaryMissing)
                return
            }
            let feed = DiskScanLineFeed(continuation)
            let process = Process()
            process.executableURL = binary
            process.arguments = args
            let stdin = Pipe()
            let stdout = Pipe()
            let stderr = Pipe()
            process.standardInput = stdin
            process.standardOutput = stdout
            process.standardError = stderr
            // Not waitUntilExit: on a GCD thread it can miss the exit and wait until the watchdog.
            let exited = DispatchSemaphore(value: 0)
            process.terminationHandler = { _ in exited.signal() }
            do {
                try process.run()
            } catch {
                continuation.finish(throwing: DiskScanError.failed("Could not start the sampler: \(error.localizedDescription)"))
                return
            }
            let pid = process.processIdentifier

            let dog = feed.startWatchdog(timeout: watchdog) {
                kill(pid, SIGTERM)
            }
            let stderrRead = DispatchGroup()
            stderrRead.enter()
            DispatchQueue.global(qos: .utility).async {
                let data = stderr.fileHandleForReading.readDataToEndOfFile()
                feed.appendStderr(data)
                stderrRead.leave()
            }
            DispatchQueue.global(qos: .utility).async {
                let handle = stdout.fileHandleForReading
                while true {
                    let chunk = handle.availableData
                    if chunk.isEmpty { break }
                    feed.feed(chunk)
                }
                exited.wait()
                dog.cancel()
                // Keep the stdin pipe open until the process ends.
                withExtendedLifetime(stdin) {}
                // The error needs all of stderr. A child that keeps stderr open must not block the end.
                _ = stderrRead.wait(timeout: .now() + 1)
                feed.end(exitCode: process.terminationStatus)
            }

            continuation.onTermination = { reason in
                guard case .cancelled = reason else { return }
                try? stdin.fileHandleForWriting.close()
                DispatchQueue.global().asyncAfter(deadline: .now() + grace) {
                    if process.isRunning { process.terminate() }
                }
            }
        }
    }
}
