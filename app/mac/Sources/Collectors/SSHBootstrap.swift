import Foundation
import NIO
import Citadel

/// Bootstrap the remote sampler over Citadel SSH/SFTP. No shell-out — uname
/// via `executeCommand`, upload via `openSFTP`, chmod via SFTP attributes,
/// then `--self-check` via `executeCommand`.
enum SSHBootstrap {
    static let remoteSamplerDir = ".towertail"
    static let remoteSamplerName = "towertail-sampler"

    /// Probes `uname -sm` on the remote and maps it to a bundled triple.
    static func detectTriple(client: SSHClient) async throws -> String {
        let out: ByteBuffer
        do {
            out = try await client.executeCommand("uname -sm")
        } catch {
            throw SamplerInvokeError.sshFailed(
                stderr: SSHErrorRenderer.describe(error), exitCode: -1
            )
        }
        let raw = String(buffer: out).trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = raw.split(separator: " ").map(String.init)
        guard parts.count == 2 else {
            throw SamplerInvokeError.misconfigured("unexpected uname output: \(raw)")
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

    /// Resolves `$HOME` on the remote — SFTP paths don't expand `~`, so we
    /// need an absolute path before opening the sampler file.
    static func resolveHome(client: SSHClient) async throws -> String {
        let out: ByteBuffer
        do {
            out = try await client.executeCommand("echo $HOME")
        } catch {
            throw SamplerInvokeError.sshFailed(
                stderr: SSHErrorRenderer.describe(error), exitCode: -1
            )
        }
        let home = String(buffer: out).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !home.isEmpty else {
            throw SamplerInvokeError.misconfigured("remote $HOME is empty")
        }
        return home
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

    /// Ensures `<home>/.towertail/` exists, uploads the binary, chmod's it
    /// 0755. Returns the absolute remote path.
    static func copyBinary(
        client: SSHClient,
        localBinary: URL
    ) async throws -> String {
        let home = try await resolveHome(client: client)
        let remoteDir = "\(home)/\(remoteSamplerDir)"
        let remotePath = "\(remoteDir)/\(remoteSamplerName)"

        // mkdir (via exec; SFTP createDirectory errors if it exists).
        _ = try? await client.executeCommand("mkdir -p \(remoteDir)")

        let sftp = try await client.openSFTP()
        defer { Task { try? await sftp.close() } }

        let data = try Data(contentsOf: localBinary)
        var attrs = SFTPFileAttributes()
        attrs.permissions = 0o755
        try await sftp.withFile(
            filePath: remotePath,
            flags: [.write, .create, .truncate],
            attributes: attrs
        ) { file in
            var buffer = ByteBufferAllocator().buffer(capacity: data.count)
            buffer.writeBytes(data)
            try await file.write(buffer)
        }
        // Some servers ignore the attribute on create — belt-and-suspenders
        // chmod via exec so the binary is guaranteed executable.
        _ = try? await client.executeCommand("chmod +x \(remotePath)")
        return remotePath
    }

    /// Runs the remote sampler with `--once` and decodes the sample.
    static func runOnce(client: SSHClient) async throws -> Sample {
        let out: ByteBuffer
        do {
            out = try await client.executeCommand("~/\(remoteSamplerDir)/\(remoteSamplerName) --once")
        } catch {
            throw SamplerInvokeError.sshFailed(
                stderr: SSHErrorRenderer.describe(error), exitCode: -1
            )
        }
        let data = Data(buffer: out)
        guard !data.isEmpty else {
            throw SamplerInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: data)
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

/// Best-effort conversion of a Citadel/NIO error into a useful one-line
/// message. `TTYSTDError` in particular wraps the actual stderr from the
/// remote command in a `ByteBuffer` — without unwrapping that, callers
/// only ever see "Citadel.TTYSTDError error 1." which tells the user
/// nothing. Returns nil when the error has no extractable detail; callers
/// fall back to `error.localizedDescription`.
enum SSHErrorRenderer {
    static func describe(_ error: Error) -> String {
        if let stderr = stderr(from: error) { return stderr }
        return error.localizedDescription
    }

    /// Extracts the stderr text from a Citadel `TTYSTDError`, or nil if
    /// the error is something else.
    static func stderr(from error: Error) -> String? {
        guard let tty = error as? TTYSTDError else { return nil }
        var buf = tty.message
        let bytes = buf.readBytes(length: buf.readableBytes) ?? []
        let raw = String(decoding: bytes, as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return raw.isEmpty ? nil : raw
    }
}

extension SSHBootstrap {
    /// End-to-end bootstrap: connect → detect triple → upload → run --once.
    static func bootstrapAndVerify(
        node: Node,
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (UUID, String) -> Void)? = nil
    ) async throws -> SSHTestReport {
        guard node.kind == .ssh,
              let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty
        else {
            throw SamplerInvokeError.misconfigured("SSH node missing user or host")
        }
        _ = (user, host)

        let nodeId = node.id
        let client = try await SSHConnectionFactory.connect(
            node: node,
            hostKeyPrompt: hostKeyPrompt,
            onTrust: onTrust.map { cb in { fp in cb(nodeId, fp) } }
        )
        defer { Task { try? await client.close() } }

        let triple = try await detectTriple(client: client)
        guard let binary = bundledBinary(forTriple: triple) else {
            throw SamplerInvokeError.misconfigured(
                "no bundled sampler for remote triple \(triple) — expected at samplers/\(triple)/towertail-sampler"
            )
        }
        let remotePath = try await copyBinary(client: client, localBinary: binary)
        let sample = try await runOnce(client: client)
        return SSHTestReport(triple: triple, remotePath: remotePath, sample: sample)
    }

    /// Update-only variant used by `SamplerUpdateCoordinator`. Connects,
    /// detects triple, uploads. Skips run-once.
    static func pushUpdate(
        node: Node,
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (UUID, String) -> Void)? = nil
    ) async throws -> String {
        guard node.kind == .ssh else {
            throw SamplerInvokeError.misconfigured("non-ssh node")
        }
        let nodeId = node.id
        let client = try await SSHConnectionFactory.connect(
            node: node,
            hostKeyPrompt: hostKeyPrompt,
            onTrust: onTrust.map { cb in { fp in cb(nodeId, fp) } }
        )
        defer { Task { try? await client.close() } }

        let triple = try await detectTriple(client: client)
        guard let binary = bundledBinary(forTriple: triple) else {
            throw SamplerInvokeError.misconfigured("no bundled sampler for triple \(triple)")
        }
        _ = try await copyBinary(client: client, localBinary: binary)
        return triple
    }
}
