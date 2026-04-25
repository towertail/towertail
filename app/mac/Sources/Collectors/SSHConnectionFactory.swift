import Foundation
import Crypto
import NIO
import NIOSSH
import Citadel

/// Callback invoked when we connect to a host without a pinned fingerprint.
/// Return `true` to trust (and remember) the key, `false` to refuse. The
/// callback runs on a background event-loop thread; UI code must hop to the
/// main actor before presenting a sheet.
typealias HostKeyPrompt = @Sendable (_ host: String, _ fingerprint: String) async -> Bool

/// Thin wrapper around `Citadel.SSHClient.connect` that centralizes the
/// auth-method branching, port defaulting, host-key verification and key
/// discovery. Kept out of the invoker/bootstrap so those files don't grow
/// every time we add an auth path.
enum SSHConnectionFactory {
    /// Raised when the pinned fingerprint differs from what the server
    /// presented. Refusing to proceed is the whole point — surface a clear
    /// error rather than swallowing it.
    struct HostKeyMismatch: LocalizedError {
        let expected: String
        let actual: String
        let host: String
        var errorDescription: String? {
            "Host key for \(host) changed (expected \(expected), got \(actual)). Connection refused."
        }
    }

    /// User (or validator) refused a new host key. Separate from mismatch so
    /// the UI can distinguish "I said no" from "something's wrong."
    struct HostKeyRejected: LocalizedError {
        let host: String
        var errorDescription: String? {
            "Host key for \(host) was not trusted."
        }
    }

    /// No usable credential was found (no keychain password, no agent, no
    /// readable key files, or only encrypted keys).
    struct NoCredential: LocalizedError {
        let reason: String
        var errorDescription: String? { reason }
    }

    /// Build the ordered list of authentication methods Citadel should try.
    /// Pure function so it's trivially unit-testable.
    /// - For `.password`: just the password method (no silent fallback to
    ///   keys — if the user picked password they mean password).
    /// - For `.key`: ed25519 first (most common modern key), then RSA; the
    ///   server iterates until one succeeds. ECDSA keys aren't supported by
    ///   Citadel's OpenSSH parser yet and are silently skipped.
    static func buildAuthMethods(
        node: Node,
        keychainPassword: String?
    ) throws -> [SSHAuthenticationMethod] {
        guard let user = node.sshUser, !user.isEmpty else {
            throw NoCredential(reason: "SSH node missing user")
        }
        switch node.authMethod {
        case .password:
            guard let pw = keychainPassword, !pw.isEmpty else {
                throw NoCredential(reason: "No password in keychain for \(node.displayName)")
            }
            return [.passwordBased(username: user, password: pw)]

        case .key:
            var methods: [SSHAuthenticationMethod] = []
            let home = FileManager.default.homeDirectoryForCurrentUser
            let sshDir = home.appendingPathComponent(".ssh", isDirectory: true)

            // ed25519
            let ed25519Path = sshDir.appendingPathComponent("id_ed25519")
            if let key = try? String(contentsOf: ed25519Path, encoding: .utf8),
               let pk = try? Curve25519.Signing.PrivateKey(sshEd25519: key) {
                methods.append(.ed25519(username: user, privateKey: pk))
            }

            // RSA
            let rsaPath = sshDir.appendingPathComponent("id_rsa")
            if let key = try? String(contentsOf: rsaPath, encoding: .utf8),
               let pk = try? Insecure.RSA.PrivateKey(sshRsa: key) {
                methods.append(.rsa(username: user, privateKey: pk))
            }

            // id_ecdsa — OpenSSH ECDSA keys aren't supported by Citadel's
            // OpenSSH parser yet. We note the file's presence in the error
            // path so users with only an ECDSA key get a clear message
            // rather than "unknown auth failure".
            if methods.isEmpty {
                let ecdsaPresent = FileManager.default.fileExists(
                    atPath: sshDir.appendingPathComponent("id_ecdsa").path
                )
                let hint = ecdsaPresent
                    ? "Only an ECDSA key was found; ECDSA OpenSSH keys aren't supported yet — use ed25519, RSA, or password auth."
                    : "No usable SSH keys found in \(sshDir.path) (checked id_ed25519, id_rsa). Encrypted keys are not yet supported — use an agent or password auth."
                throw NoCredential(reason: hint)
            }
            return methods
        }
    }

    /// SHA256 fingerprint of a NIOSSHPublicKey, formatted like `ssh` prints:
    /// base64 without padding, prefixed with `SHA256:`.
    static func fingerprint(of key: NIOSSHPublicKey) -> String {
        var buf = ByteBufferAllocator().buffer(capacity: 512)
        _ = key.write(to: &buf)
        let bytes = buf.getBytes(at: 0, length: buf.readableBytes) ?? []
        let hash = SHA256.hash(data: bytes)
        let b64 = Data(hash).base64EncodedString().replacingOccurrences(of: "=", with: "")
        return "SHA256:\(b64)"
    }

    /// Host key validator that enforces the pinned fingerprint on the node.
    /// When nil, calls `prompt` once; if accepted, `onTrust` is invoked with
    /// the accepted fingerprint so the caller can persist it on the Node.
    final class FingerprintValidator: NIOSSHClientServerAuthenticationDelegate, Sendable {
        let host: String
        let pinned: String?
        let prompt: HostKeyPrompt?
        let onTrust: (@Sendable (String) -> Void)?

        init(host: String, pinned: String?, prompt: HostKeyPrompt?, onTrust: (@Sendable (String) -> Void)?) {
            self.host = host
            self.pinned = pinned
            self.prompt = prompt
            self.onTrust = onTrust
        }

        func validateHostKey(
            hostKey: NIOSSHPublicKey,
            validationCompletePromise: EventLoopPromise<Void>
        ) {
            let fp = SSHConnectionFactory.fingerprint(of: hostKey)
            if let pinned {
                if fp == pinned {
                    validationCompletePromise.succeed(())
                } else {
                    validationCompletePromise.fail(
                        HostKeyMismatch(expected: pinned, actual: fp, host: host)
                    )
                }
                return
            }
            // No pin yet — TOFU. If no prompt was supplied (tests, headless
            // bootstrap), refuse rather than silently trusting anything.
            guard let prompt else {
                validationCompletePromise.fail(HostKeyRejected(host: host))
                return
            }
            let host = self.host
            let onTrust = self.onTrust
            Task.detached {
                let accepted = await prompt(host, fp)
                if accepted {
                    onTrust?(fp)
                    validationCompletePromise.succeed(())
                } else {
                    validationCompletePromise.fail(HostKeyRejected(host: host))
                }
            }
        }
    }

    /// One-shot connect helper: builds auth + validator, connects, returns
    /// the live client. Caller owns the client and is responsible for
    /// closing it. Port defaults to `node.effectiveSshPort`.
    static func connect(
        node: Node,
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (String) -> Void)? = nil,
        passwordProvider: @Sendable (UUID) -> String? = { id in
            (try? KeychainStore.getPassword(for: id)) ?? nil
        }
    ) async throws -> SSHClient {
        guard node.kind == .ssh,
              let host = node.sshHost, !host.isEmpty else {
            throw SamplerInvokeError.misconfigured("SSH node missing host")
        }
        let port = node.effectiveSshPort
        let password = node.authMethod == .password ? passwordProvider(node.id) : nil
        let methods = try buildAuthMethods(node: node, keychainPassword: password)

        let validator = FingerprintValidator(
            host: host,
            pinned: node.knownHostFingerprint,
            prompt: hostKeyPrompt,
            onTrust: onTrust
        )

        // Try each auth method in order. Citadel accepts a single
        // SSHAuthenticationMethod; we retry on auth failure for the key path.
        var lastError: Error?
        for method in methods {
            do {
                // Inject a tiny channel handler that, once the TCP socket
                // is up, tightens keepalive on the underlying file
                // descriptor. macOS defaults are very long (~2hr idle),
                // which would let a half-open flow keep a remote sshd +
                // sampler alive indefinitely if our laptop sleeps or NAT
                // drops the flow. ~30s idle / 10s probe / 4 probes is
                // noticed-dead within ~70s.
                let client = try await SSHClient.connect(
                    host: host,
                    port: port,
                    authenticationMethod: method,
                    hostKeyValidator: .custom(validator),
                    reconnect: .never,
                    channelHandlers: [TCPKeepaliveHandler()]
                )
                return client
            } catch {
                // Host-key errors should surface immediately — retrying with
                // a different auth method won't help.
                if error is HostKeyMismatch || error is HostKeyRejected {
                    throw error
                }
                lastError = error
                continue
            }
        }
        // Translate Citadel's opaque "SSHClientError error 4" (all auth
        // options failed) into something the user can act on.
        if let last = lastError, isAllAuthFailed(last) && node.authMethod == .key {
            throw NoCredential(reason:
                "\(host): all SSH keys rejected. Citadel's RSA auth uses SHA-1, " +
                "which modern OpenSSH servers disable. Fix options: " +
                "(1) generate an ed25519 key — `ssh-keygen -t ed25519` + add to server's authorized_keys, " +
                "(2) switch this host to password auth, or " +
                "(3) re-enable legacy RSA on the server (PubkeyAcceptedAlgorithms +ssh-rsa).")
        }
        throw lastError ?? NoCredential(reason: "SSH authentication failed")
    }

    /// Tightens TCP keepalive on the underlying socket once the channel
    /// is active. Sendable because it carries no state — each connect
    /// gets its own instance via the `channelHandlers` array.
    final class TCPKeepaliveHandler: ChannelInboundHandler, Sendable {
        typealias InboundIn = Any
        typealias InboundOut = Any

        func channelActive(context: ChannelHandlerContext) {
            let channel = context.channel
            // Errors on these are best-effort — if the kernel doesn't
            // accept the option (older macOS, sandbox restriction), we
            // still get the OS default keepalive behavior, which is just
            // less aggressive than what we asked for.
            _ = channel.setOption(ChannelOptions.socket(SocketOptionLevel(SOL_SOCKET), SO_KEEPALIVE), value: 1)
            _ = channel.setOption(ChannelOptions.socket(SocketOptionLevel(IPPROTO_TCP), TCP_KEEPALIVE), value: 30)
            _ = channel.setOption(ChannelOptions.socket(SocketOptionLevel(IPPROTO_TCP), TCP_KEEPINTVL), value: 10)
            _ = channel.setOption(ChannelOptions.socket(SocketOptionLevel(IPPROTO_TCP), TCP_KEEPCNT), value: 4)
            context.fireChannelActive()
        }
    }

    private static func isAllAuthFailed(_ error: Error) -> Bool {
        let ns = error as NSError
        // Citadel.SSHClientError is a plain Swift enum; its NSError bridge
        // carries domain "Citadel.SSHClientError" and code 4 for
        // `allAuthenticationOptionsFailed`.
        return ns.domain.contains("SSHClientError") && ns.code == 4
    }
}
