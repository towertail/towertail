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
        defer { Task { try? await client.close() } }

        do {
            let output = try await client.executeCommand("\(Self.remoteSamplerPath) --once")
            let data = Data(buffer: output)
            guard !data.isEmpty else {
                throw SamplerInvokeError.emptyOutput
            }
            return try SampleCodec.decoder().decode(Sample.self, from: data)
        } catch let err as SamplerInvokeError {
            throw err
        } catch let err as DecodingError {
            throw SamplerInvokeError.decodeFailed(underlying: err)
        } catch {
            throw SamplerInvokeError.sshFailed(
                stderr: error.localizedDescription, exitCode: -1
            )
        }
    }
}
