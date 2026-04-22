import Foundation

struct SSHSamplerInvoker: SamplerInvoker {
    static let sshExecutable = URL(fileURLWithPath: "/usr/bin/ssh")
    static let remoteSamplerPath = "~/.towertail/towertail-sampler"

    func invokeOnce(node: Node) async throws -> Sample {
        guard node.kind == .ssh else {
            throw SamplerInvokeError.misconfigured("SSHSamplerInvoker called for non-ssh node")
        }
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty else {
            throw SamplerInvokeError.misconfigured("SSH node missing user or host")
        }

        // ServerAlive* + TCPKeepAlive make ssh detect a dead socket quickly
        // after Mac wake or Wi‑Fi flap — otherwise a wake-broken connection
        // can hang until the OS-level TCP retransmit timeout (~minutes)
        // rather than the 10s ProcessRunner budget.
        let args = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=5",
            "-o", "ServerAliveInterval=15",
            "-o", "ServerAliveCountMax=3",
            "-o", "TCPKeepAlive=yes",
            "-o", "StrictHostKeyChecking=accept-new",
            "\(user)@\(host)",
            "\(Self.remoteSamplerPath) --once"
        ]

        let result = try await ProcessRunner.run(
            executable: Self.sshExecutable,
            arguments: args
        )
        if result.exitCode != 0 {
            let err = String(data: result.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: result.exitCode)
        }
        guard !result.stdout.isEmpty else {
            throw SamplerInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: result.stdout)
        } catch {
            throw SamplerInvokeError.decodeFailed(underlying: error)
        }
    }
}
