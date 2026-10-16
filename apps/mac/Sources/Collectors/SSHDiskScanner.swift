import Foundation
import NIO
import Citadel

/// Runs `~/.towertail/towertail-sampler scan …` on an SSH exec channel and
/// streams its stdout. The channel keeps stdin open. Cancel closes the
/// connection: sshd then closes the stdin of the sampler, and the sampler
/// stops. There is no fixed timeout: the watchdog fails the scan when no
/// line comes for `watchdog` seconds.
struct SSHDiskScanner: DiskScanner {
    let hostKeyPrompt: HostKeyPrompt?
    let onTrust: (@Sendable (UUID, String) -> Void)?
    /// Runs before the scan with the output of `--version`. It makes sure
    /// that the host has a sampler that knows `scan`.
    let prepare: (@Sendable (Node, String) async throws -> Void)?
    var watchdog: TimeInterval = 60

    init(
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (UUID, String) -> Void)? = nil,
        prepare: (@Sendable (Node, String) async throws -> Void)? = nil,
        watchdog: TimeInterval = 60
    ) {
        self.hostKeyPrompt = hostKeyPrompt
        self.onTrust = onTrust
        self.prepare = prepare
        self.watchdog = watchdog
    }

    func scan(node: Node, request: DiskScanRequest) -> AsyncThrowingStream<DiskScanEvent, Error> {
        let prompt = hostKeyPrompt
        let onTrust = self.onTrust
        let prepare = self.prepare
        let watchdog = self.watchdog
        return AsyncThrowingStream { continuation in
            let feed = DiskScanLineFeed(continuation)
            let task = Task {
                guard node.kind == .ssh else {
                    feed.fail(DiskScanError.misconfigured("SSHDiskScanner called for a node that is not SSH"))
                    return
                }
                let nodeId = node.id
                let client: SSHClient
                do {
                    client = try await SSHConnectionFactory.connect(
                        node: node,
                        hostKeyPrompt: prompt,
                        onTrust: onTrust.map { cb in { fp in cb(nodeId, fp) } }
                    )
                } catch {
                    feed.fail(DiskScanError.failed(SSHErrorRenderer.describe(error)))
                    return
                }
                do {
                    if let prepare {
                        let out = try? await client.executeCommand(
                            "\(SSHSamplerInvoker.remoteSamplerPath) --version", mergeStreams: true
                        )
                        try await prepare(node, out.map { String(buffer: $0) } ?? "")
                    }
                    try Task.checkCancellation()
                    let command = request.shellCommand(binary: SSHSamplerInvoker.remoteSamplerPath)
                    let stream = try await client.executeCommandStream(command)
                    let dog = feed.startWatchdog(timeout: watchdog) {
                        Task.detached { try? await client.close() }
                    }
                    defer { dog.cancel() }
                    for try await chunk in stream {
                        switch chunk {
                        case .stdout(let buf): feed.feed(Data(buffer: buf))
                        case .stderr(let buf): feed.appendStderr(Data(buffer: buf))
                        }
                    }
                    if !Task.isCancelled { feed.end(exitCode: 0) }
                } catch let err as SSHClient.CommandFailed {
                    feed.end(exitCode: Int32(clamping: err.exitCode))
                } catch is CancellationError {
                    feed.fail(CancellationError())
                } catch {
                    feed.fail(DiskScanError.failed(SSHErrorRenderer.describe(error)))
                }
                await Task.detached { try? await client.close() }.value
            }
            continuation.onTermination = { reason in
                if case .cancelled = reason { task.cancel() }
            }
        }
    }
}
