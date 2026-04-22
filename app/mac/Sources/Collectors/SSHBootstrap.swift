import Foundation

enum SSHBootstrap {
    static let sshExecutable = URL(fileURLWithPath: "/usr/bin/ssh")
    static let scpExecutable = URL(fileURLWithPath: "/usr/bin/scp")
    static let remoteSamplerDir = "~/.towertail"
    static let remoteSamplerPath = "~/.towertail/towertail-sampler"

    static let commonFlags: [String] = [
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=5",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        "-o", "TCPKeepAlive=yes",
        "-o", "StrictHostKeyChecking=accept-new",
    ]

    /// Probes `uname -sm` on the remote and maps it to a bundled triple.
    /// Returns nil if the OS/arch combo has no bundled binary.
    static func detectTriple(user: String, host: String) async throws -> String {
        let args = commonFlags + ["\(user)@\(host)", "uname -sm"]
        let r = try await ProcessRunner.run(executable: sshExecutable, arguments: args)
        if r.exitCode != 0 {
            let err = String(data: r.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: r.exitCode)
        }
        let out = String(data: r.stdout, encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let parts = out.split(separator: " ").map(String.init)
        guard parts.count == 2 else {
            throw SamplerInvokeError.misconfigured("unexpected uname output: \(out)")
        }
        let os = parts[0].lowercased()
        let arch = parts[1].lowercased()
        let goos: String
        switch os {
        case "darwin": goos = "darwin"
        case "linux": goos = "linux"
        default: throw SamplerInvokeError.misconfigured("unsupported remote OS: \(os)")
        }
        let goarch: String
        switch arch {
        case "arm64", "aarch64": goarch = "arm64"
        case "x86_64", "amd64": goarch = "amd64"
        default: throw SamplerInvokeError.misconfigured("unsupported remote arch: \(arch)")
        }
        return "\(goos)-\(goarch)"
    }

    /// Locates the bundled sampler binary for `triple` inside Towertail.app/Contents/Resources/samplers/.
    static func bundledBinary(forTriple triple: String) -> URL? {
        if let url = Bundle.main.url(
            forResource: "towertail-sampler",
            withExtension: nil,
            subdirectory: "samplers/\(triple)"
        ) {
            return url
        }
        if let resourceURL = Bundle.main.resourceURL {
            let candidate = resourceURL
                .appendingPathComponent("samplers", isDirectory: true)
                .appendingPathComponent(triple, isDirectory: true)
                .appendingPathComponent("towertail-sampler")
            if FileManager.default.fileExists(atPath: candidate.path) {
                return candidate
            }
        }
        // Development fallback — walk up to find dist/samplers/<triple>/towertail-sampler
        let fm = FileManager.default
        var url = URL(fileURLWithPath: fm.currentDirectoryPath)
        for _ in 0..<8 {
            let candidate = url.appendingPathComponent("dist/samplers/\(triple)/towertail-sampler")
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
    ///
    /// Timeouts are generous (30s each stage, 60s for the scp itself) to
    /// tolerate marginal hosts — slow SSH negotiation, small uplink, or
    /// busy kernels — without silently dropping the push.
    static func copyBinary(
        localBinary: URL,
        user: String,
        host: String,
        timeout: TimeInterval = 60
    ) async throws -> String {
        // mkdir -p ~/.towertail
        let mkdirArgs = commonFlags + ["\(user)@\(host)", "mkdir -p \(remoteSamplerDir)"]
        let mk = try await ProcessRunner.run(
            executable: sshExecutable, arguments: mkdirArgs, timeout: 30
        )
        if mk.exitCode != 0 {
            let err = String(data: mk.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: mk.exitCode)
        }

        // scp localBinary user@host:~/.towertail/towertail-sampler
        let scpArgs = commonFlags + [
            localBinary.path,
            "\(user)@\(host):\(remoteSamplerPath)",
        ]
        let scp = try await ProcessRunner.run(
            executable: scpExecutable,
            arguments: scpArgs,
            timeout: timeout
        )
        if scp.exitCode != 0 {
            let err = String(data: scp.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: scp.exitCode)
        }

        // chmod +x ~/.towertail/towertail-sampler
        let chmodArgs = commonFlags + ["\(user)@\(host)", "chmod +x \(remoteSamplerPath)"]
        let ch = try await ProcessRunner.run(
            executable: sshExecutable, arguments: chmodArgs, timeout: 30
        )
        if ch.exitCode != 0 {
            let err = String(data: ch.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: ch.exitCode)
        }
        return remoteSamplerPath
    }

    /// Runs `~/.towertail/towertail-sampler --once` over ssh and decodes the sample.
    static func runOnce(user: String, host: String) async throws -> Sample {
        let args = commonFlags + ["\(user)@\(host)", "\(remoteSamplerPath) --once"]
        let r = try await ProcessRunner.run(executable: sshExecutable, arguments: args)
        if r.exitCode != 0 {
            let err = String(data: r.stderr, encoding: .utf8) ?? ""
            throw SamplerInvokeError.sshFailed(stderr: err, exitCode: r.exitCode)
        }
        guard !r.stdout.isEmpty else {
            throw SamplerInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: r.stdout)
        } catch {
            throw SamplerInvokeError.decodeFailed(underlying: error)
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
            throw SamplerInvokeError.misconfigured("SSH node missing user or host")
        }
        let triple = try await detectTriple(user: user, host: host)
        guard let binary = bundledBinary(forTriple: triple) else {
            throw SamplerInvokeError.misconfigured(
                "no bundled sampler for remote triple \(triple) — expected at samplers/\(triple)/towertail-sampler"
            )
        }
        let remotePath = try await copyBinary(localBinary: binary, user: user, host: host)
        let sample = try await runOnce(user: user, host: host)
        return SSHTestReport(triple: triple, remotePath: remotePath, sample: sample)
    }
}
