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
            DispatchQueue.global(qos: .utility).async {
                let data = stderr.fileHandleForReading.readDataToEndOfFile()
                feed.appendStderr(data)
            }
            DispatchQueue.global(qos: .utility).async {
                let handle = stdout.fileHandleForReading
                while true {
                    let chunk = handle.availableData
                    if chunk.isEmpty { break }
                    feed.feed(chunk)
                }
                process.waitUntilExit()
                dog.cancel()
                // Keep the stdin pipe open until the process ends.
                withExtendedLifetime(stdin) {}
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
