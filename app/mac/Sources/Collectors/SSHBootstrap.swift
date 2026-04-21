import Foundation

enum SSHBootstrap {
    static let sshExecutable = URL(fileURLWithPath: "/usr/bin/ssh")
    static let scpExecutable = URL(fileURLWithPath: "/usr/bin/scp")
    static let remoteAgentDir = "~/.towertail"
    static let remoteAgentPath = "~/.towertail/agent"

    static let commonFlags: [String] = [
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=5",
        "-o", "StrictHostKeyChecking=accept-new",
    ]

    /// Probes `uname -sm` on the remote and maps it to a bundled triple.
    /// Returns nil if the OS/arch combo has no bundled binary.
    static func detectTriple(user: String, host: String) async throws -> String {
        let args = commonFlags + ["\(user)@\(host)", "uname -sm"]
        let r = try await ProcessRunner.run(executable: sshExecutable, arguments: args)
        if r.exitCode != 0 {
            let err = String(data: r.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: r.exitCode)
        }
        let out = String(data: r.stdout, encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let parts = out.split(separator: " ").map(String.init)
        guard parts.count == 2 else {
            throw AgentInvokeError.misconfigured("unexpected uname output: \(out)")
        }
        let os = parts[0].lowercased()
        let arch = parts[1].lowercased()
        let goos: String
        switch os {
        case "darwin": goos = "darwin"
        case "linux": goos = "linux"
        default: throw AgentInvokeError.misconfigured("unsupported remote OS: \(os)")
        }
        let goarch: String
        switch arch {
        case "arm64", "aarch64": goarch = "arm64"
        case "x86_64", "amd64": goarch = "amd64"
        default: throw AgentInvokeError.misconfigured("unsupported remote arch: \(arch)")
        }
        return "\(goos)-\(goarch)"
    }

    /// Locates the bundled agent binary for `triple` inside Towertail.app/Contents/Resources/agents/.
    static func bundledBinary(forTriple triple: String) -> URL? {
        if let url = Bundle.main.url(
            forResource: "towertail-agent",
            withExtension: nil,
            subdirectory: "agents/\(triple)"
        ) {
            return url
        }
        if let resourceURL = Bundle.main.resourceURL {
            let candidate = resourceURL
                .appendingPathComponent("agents", isDirectory: true)
                .appendingPathComponent(triple, isDirectory: true)
                .appendingPathComponent("towertail-agent")
            if FileManager.default.fileExists(atPath: candidate.path) {
                return candidate
            }
        }
        // Development fallback — walk up to find dist/agents/<triple>/towertail-agent
        let fm = FileManager.default
        var url = URL(fileURLWithPath: fm.currentDirectoryPath)
        for _ in 0..<8 {
            let candidate = url.appendingPathComponent("dist/agents/\(triple)/towertail-agent")
            if fm.fileExists(atPath: candidate.path) {
                return candidate
            }
            let parent = url.deletingLastPathComponent()
            if parent.path == url.path { break }
            url = parent
        }
        return nil
    }

    /// Ensures the remote `~/.towertail/` directory exists and copies the local
    /// binary into it, marking it executable. Returns the remote path.
    static func copyBinary(
        localBinary: URL,
        user: String,
        host: String,
        timeout: TimeInterval = 30
    ) async throws -> String {
        // mkdir -p ~/.towertail
        let mkdirArgs = commonFlags + ["\(user)@\(host)", "mkdir -p \(remoteAgentDir)"]
        let mk = try await ProcessRunner.run(executable: sshExecutable, arguments: mkdirArgs)
        if mk.exitCode != 0 {
            let err = String(data: mk.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: mk.exitCode)
        }

        // scp localBinary user@host:~/.towertail/agent
        let scpArgs = commonFlags + [
            localBinary.path,
            "\(user)@\(host):\(remoteAgentPath)",
        ]
        let scp = try await ProcessRunner.run(
            executable: scpExecutable,
            arguments: scpArgs,
            timeout: timeout
        )
        if scp.exitCode != 0 {
            let err = String(data: scp.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: scp.exitCode)
        }

        // chmod +x ~/.towertail/agent
        let chmodArgs = commonFlags + ["\(user)@\(host)", "chmod +x \(remoteAgentPath)"]
        let ch = try await ProcessRunner.run(executable: sshExecutable, arguments: chmodArgs)
        if ch.exitCode != 0 {
            let err = String(data: ch.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: ch.exitCode)
        }
        return remoteAgentPath
    }

    /// Runs `~/.towertail/agent --once` over ssh and decodes the sample.
    static func runOnce(user: String, host: String) async throws -> Sample {
        let args = commonFlags + ["\(user)@\(host)", "\(remoteAgentPath) --once"]
        let r = try await ProcessRunner.run(executable: sshExecutable, arguments: args)
        if r.exitCode != 0 {
            let err = String(data: r.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: r.exitCode)
        }
        guard !r.stdout.isEmpty else {
            throw AgentInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: r.stdout)
        } catch {
            throw AgentInvokeError.decodeFailed(underlying: error)
        }
    }
}

/// Result returned to the UI for a SCP-and-run Test action.
struct SSHTestReport: Sendable {
    let triple: String
    let remotePath: String
    let sample: Sample
}

extension SSHBootstrap {
    /// End-to-end bootstrap: detect triple → scp the matching bundled binary → run --once.
    static func bootstrapAndVerify(node: Node) async throws -> SSHTestReport {
        guard node.kind == .ssh,
              let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty
        else {
            throw AgentInvokeError.misconfigured("SSH node missing user or host")
        }
        let triple = try await detectTriple(user: user, host: host)
        guard let binary = bundledBinary(forTriple: triple) else {
            throw AgentInvokeError.misconfigured(
                "no bundled agent for remote triple \(triple) — expected at agents/\(triple)/towertail-agent"
            )
        }
        let remotePath = try await copyBinary(localBinary: binary, user: user, host: host)
        let sample = try await runOnce(user: user, host: host)
        return SSHTestReport(triple: triple, remotePath: remotePath, sample: sample)
    }
}
