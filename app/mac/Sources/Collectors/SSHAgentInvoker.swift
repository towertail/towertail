import Foundation

struct SSHAgentInvoker: AgentInvoker {
    static let sshExecutable = URL(fileURLWithPath: "/usr/bin/ssh")
    static let remoteAgentPath = "~/.towertail/agent"

    func invokeOnce(node: Node) async throws -> Sample {
        guard node.kind == .ssh else {
            throw AgentInvokeError.misconfigured("SSHAgentInvoker called for non-ssh node")
        }
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty else {
            throw AgentInvokeError.misconfigured("SSH node missing user or host")
        }

        let args = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=5",
            "-o", "StrictHostKeyChecking=accept-new",
            "\(user)@\(host)",
            "\(Self.remoteAgentPath) --once"
        ]

        let result = try await ProcessRunner.run(
            executable: Self.sshExecutable,
            arguments: args
        )
        if result.exitCode != 0 {
            let err = String(data: result.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: result.exitCode)
        }
        guard !result.stdout.isEmpty else {
            throw AgentInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: result.stdout)
        } catch {
            throw AgentInvokeError.decodeFailed(underlying: error)
        }
    }
}
