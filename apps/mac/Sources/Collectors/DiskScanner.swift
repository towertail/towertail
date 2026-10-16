import Foundation

/// Runs `towertail-sampler scan` for a node and streams its events.
/// Cancel the consuming task to stop the scan.
protocol DiskScanner: Sendable {
    func scan(node: Node, request: DiskScanRequest) -> AsyncThrowingStream<DiskScanEvent, Error>
}

/// Scanner for a node of the local backend. Remote mode uses
/// `RemoteBackend.diskScanner(for:)`.
@Sendable
func makeDiskScanner(
    for node: Node,
    prepareSSH: (@Sendable (Node, String) async throws -> Void)? = nil
) -> any DiskScanner {
    switch node.kind {
    case .local:
        return LocalDiskScanner()
    case .ssh:
        return SSHDiskScanner(
            hostKeyPrompt: HostKeyTrustPrompter.sharedPromptAdapter,
            onTrust: { id, fp in
                Task { @MainActor in HostKeyTrustPersister.persist(nodeId: id, fingerprint: fp) }
            },
            prepare: prepareSSH
        )
    }
}

/// Splits stdout bytes into NDJSON lines and sends the decoded events to
/// a stream. It also runs the no-output watchdog. Thread-safe.
final class DiskScanLineFeed: @unchecked Sendable {
    static let maxStderr = 8 * 1024

    private let lock = NSLock()
    private let continuation: AsyncThrowingStream<DiskScanEvent, Error>.Continuation
    private var buffer = Data()
    private var stderr = Data()
    private var done = false
    private var lastActivity = Date()
    private var onStall: (@Sendable () -> Void)?

    init(_ continuation: AsyncThrowingStream<DiskScanEvent, Error>.Continuation) {
        self.continuation = continuation
    }

    var isDone: Bool { lock.withLock { done } }

    func feed(_ data: Data) {
        lock.lock()
        defer { lock.unlock() }
        guard !done else { return }
        lastActivity = Date()
        buffer.append(data)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer[buffer.startIndex..<nl]
            buffer.removeSubrange(buffer.startIndex...nl)
            emitLocked(Data(line))
            if done { return }
        }
    }

    func appendStderr(_ data: Data) {
        lock.withLock {
            guard stderr.count < Self.maxStderr else { return }
            stderr.append(data.prefix(Self.maxStderr - stderr.count))
        }
    }

    /// Call when the process ended. A missing final line is an error.
    func end(exitCode: Int32) {
        lock.lock()
        defer { lock.unlock() }
        guard !done else { return }
        if !buffer.isEmpty {
            let rest = buffer
            buffer.removeAll()
            emitLocked(rest)
            if done { return }
        }
        let err = String(decoding: stderr, as: UTF8.self)
        if exitCode != 0 {
            finishLocked(throwing: DiskScanError.exited(code: exitCode, stderr: err))
        } else {
            finishLocked(throwing: DiskScanError.failed("The sampler stopped without a result."))
        }
    }

    func fail(_ error: Error) {
        lock.withLock {
            guard !done else { return }
            finishLocked(throwing: error)
        }
    }

    /// Fails the scan when no stdout comes for `timeout` seconds. `onStall`
    /// stops the process or channel.
    func startWatchdog(timeout: TimeInterval, onStall: @escaping @Sendable () -> Void) -> Task<Void, Never> {
        lock.withLock {
            lastActivity = Date()
            self.onStall = onStall
        }
        let tick = UInt64(min(max(timeout / 4, 0.05), 1) * 1_000_000_000)
        return Task.detached { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: tick)
                guard let self else { return }
                if self.checkStall(timeout: timeout) { return }
            }
        }
    }

    private func checkStall(timeout: TimeInterval) -> Bool {
        let stall: (@Sendable () -> Void)?
        lock.lock()
        if done {
            lock.unlock()
            return true
        }
        guard Date().timeIntervalSince(lastActivity) >= timeout else {
            lock.unlock()
            return false
        }
        finishLocked(throwing: DiskScanError.noOutput(seconds: Int(timeout.rounded())))
        stall = onStall
        lock.unlock()
        stall?()
        return true
    }

    private func emitLocked(_ line: Data) {
        let trimmed = line.drop(while: { $0 == 0x20 || $0 == 0x0D })
        guard !trimmed.isEmpty, trimmed.contains(where: { $0 != 0x20 && $0 != 0x0D }) else { return }
        let event: DiskScanEvent
        do {
            event = try DiskScanEvent.decodeLine(Data(trimmed))
        } catch {
            finishLocked(throwing: error)
            return
        }
        continuation.yield(event)
        if event.isFinal { finishLocked(throwing: nil) }
    }

    private func finishLocked(throwing error: Error?) {
        done = true
        if let error { continuation.finish(throwing: error) } else { continuation.finish() }
    }
}
