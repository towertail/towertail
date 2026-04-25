import Foundation
import NIO
import Citadel

/// Invokes `towertail-sampler --once` on a remote host over SSH via Citadel.
/// No shell-out — connect, exec, collect stdout, decode.
struct SSHSamplerInvoker: SamplerInvoker {
    static let remoteSamplerPath = "~/.towertail/towertail-sampler"

    /// Optional host-key prompt. When nil (the default for unit tests) and
    /// the Node has no pinned fingerprint, the connect refuses rather than
    /// trusting anything. The production wiring injects a prompt that pops
    /// a sheet on first connect.
    let hostKeyPrompt: HostKeyPrompt?

    /// Called when the user accepts a new host key. Wired to write the
    /// fingerprint back onto the Node so later connects use the pinned path.
    let onTrust: (@Sendable (UUID, String) -> Void)?

    init(
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (UUID, String) -> Void)? = nil
    ) {
        self.hostKeyPrompt = hostKeyPrompt
        self.onTrust = onTrust
    }

    func invokeOnce(node: Node) async throws -> Sample {
        guard node.kind == .ssh else {
            throw SamplerInvokeError.misconfigured("SSHSamplerInvoker called for non-ssh node")
        }
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty else {
            throw SamplerInvokeError.misconfigured("SSH node missing user or host")
        }
        _ = user
        _ = host

        let nodeId = node.id
        let onTrust = self.onTrust
        let client = try await SSHConnectionFactory.connect(
            node: node,
            hostKeyPrompt: hostKeyPrompt,
            onTrust: onTrust.map { cb in
                { (fp: String) in cb(nodeId, fp) }
            }
        )

        // Structured close on the way out. The previous fire-and-forget
        // `Task { try? await client.close() }` could be dropped on the
        // floor when the parent pacer was canceled, leaving the SSH
        // channel ESTABLISHED and the FD leaking. Detach so cancellation
        // of the parent doesn't propagate, but still await so we don't
        // return until the channel is torn down.
        let result: Result<Sample, Error>
        do {
            let output = try await client.executeCommand("\(Self.remoteSamplerPath) --once")
            let data = Data(buffer: output)
            guard !data.isEmpty else {
                throw SamplerInvokeError.emptyOutput
            }
            result = .success(try SampleCodec.decoder().decode(Sample.self, from: data))
        } catch let err as SamplerInvokeError {
            result = .failure(err)
        } catch let err as DecodingError {
            result = .failure(SamplerInvokeError.decodeFailed(underlying: err))
        } catch {
            result = .failure(SamplerInvokeError.sshFailed(
                stderr: error.localizedDescription, exitCode: -1
            ))
        }
        await Task.detached { try? await client.close() }.value
        switch result {
        case .success(let s): return s
        case .failure(let e): throw e
        }
    }
}
