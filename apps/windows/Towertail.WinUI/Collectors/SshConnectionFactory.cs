using System.Security.Cryptography;
using Renci.SshNet;
using Renci.SshNet.Common;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// User decision for a first-time host-key prompt. Returns true to trust (and
/// persist) the key, false to refuse. Async because the WinUI implementation
/// pops a <c>ContentDialog</c> that awaits user input.
/// </summary>
public delegate Task<bool> HostKeyPrompt(string host, string fingerprint);

/// <summary>
/// Centralizes SSH connection setup for both <see cref="SshSamplerInvoker"/>
/// and <see cref="SshBootstrap"/>. Pure building-block helpers live as static
/// methods so unit tests can exercise them without opening a socket.
/// </summary>
public static class SshConnectionFactory
{
    /// <summary>Pinned fingerprint didn't match what the server presented.</summary>
    public sealed class HostKeyMismatchException : Exception
    {
        public string Host { get; }
        public string Expected { get; }
        public string Actual { get; }
        public HostKeyMismatchException(string host, string expected, string actual)
            : base($"Host key for {host} changed (expected {expected}, got {actual}). Connection refused.")
        {
            Host = host; Expected = expected; Actual = actual;
        }
    }

    /// <summary>User/validator refused a new key; distinct from mismatch.</summary>
    public sealed class HostKeyRejectedException : Exception
    {
        public string Host { get; }
        public HostKeyRejectedException(string host)
            : base($"Host key for {host} was not trusted.")
        { Host = host; }
    }

    /// <summary>No usable credential found (no password in DPAPI, no keys, only encrypted keys).</summary>
    public sealed class NoCredentialException : Exception
    {
        public NoCredentialException(string message) : base(message) { }
    }

    /// <summary>
    /// Build the list of AuthenticationMethod objects SSH.NET should try, in
    /// the order it should try them. Pure — unit-testable without a network.
    /// </summary>
    public static AuthenticationMethod[] BuildAuthMethods(
        Node node,
        string? dpapiPassword,
        string? sshDir = null)
    {
        if (string.IsNullOrEmpty(node.SshUser))
            throw new NoCredentialException("SSH node missing user");

        if (node.AuthMethod == AuthMethod.Password)
        {
            if (string.IsNullOrEmpty(dpapiPassword))
                throw new NoCredentialException($"No password in credentials store for {node.DisplayName}");
            return new AuthenticationMethod[] { new PasswordAuthenticationMethod(node.SshUser, dpapiPassword) };
        }

        var methods = new List<AuthenticationMethod>();
        var dir = sshDir ?? Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), ".ssh");

        // Iterate the standard key files. SSH.NET's PrivateKeyFile parser
        // handles both OpenSSH and PEM formats for ed25519 / ecdsa / rsa.
        foreach (var name in new[] { "id_ed25519", "id_ecdsa", "id_rsa" })
        {
            var path = Path.Combine(dir, name);
            if (!File.Exists(path)) continue;
            try
            {
                var keyFile = new PrivateKeyFile(path);
                methods.Add(new PrivateKeyAuthenticationMethod(node.SshUser, keyFile));
            }
            catch (SshPassPhraseNullOrEmptyException)
            {
                // Encrypted — defer handling. The agent path (when available)
                // or a fallback key is likely enough. Swallow; don't throw.
            }
            catch
            {
                // Unparseable key file; skip silently.
            }
        }

        if (methods.Count == 0)
        {
            throw new NoCredentialException(
                $"No usable SSH keys found in {dir}. Encrypted private keys aren't supported yet — use an agent or password auth.");
        }

        return methods.ToArray();
    }

    /// <summary>
    /// SHA256 fingerprint of an SSH host key byte blob, formatted like
    /// <c>ssh-keygen -lf</c>: base64 without padding, prefixed with "SHA256:".
    /// </summary>
    public static string Fingerprint(byte[] hostKey)
    {
        var hash = SHA256.HashData(hostKey);
        return "SHA256:" + Convert.ToBase64String(hash).TrimEnd('=');
    }

    /// <summary>
    /// Wire the <see cref="Renci.SshNet.Common.HostKeyEventArgs.CanTrust"/>
    /// callback for <paramref name="client"/>. Trusts only if the pinned
    /// fingerprint matches or the prompt (if provided) accepts a new key.
    /// </summary>
    /// <param name="onTrust">
    /// Invoked with the newly-accepted fingerprint when <paramref name="pinned"/>
    /// is null and the prompt returns true. Used to persist the fingerprint
    /// back onto the Node.
    /// </param>
    public static void WireHostKeyValidation(
        BaseClient client,
        string host,
        string? pinned,
        HostKeyPrompt? prompt,
        Action<string>? onTrust)
    {
        client.HostKeyReceived += (_, e) =>
        {
            var fp = Fingerprint(e.HostKey);
            if (!string.IsNullOrEmpty(pinned))
            {
                if (fp == pinned) { e.CanTrust = true; return; }
                e.CanTrust = false;
                // SSH.NET doesn't surface the mismatch as an exception by itself
                // — it just refuses to connect with "Server response does not
                // contain SSH identification". Stash the mismatch on a tag so
                // the caller can translate into a clearer error after Connect
                // throws. We use the Client's Connection closing path below.
                throw new HostKeyMismatchException(host, pinned, fp);
            }
            if (prompt is null) { e.CanTrust = false; return; }

            // Prompt is async; block this callback (it's on a worker thread,
            // not UI) and wait. Yes, `.Result`. The alternative is letting
            // the connect proceed and re-checking after, which is worse.
            bool accepted;
            try
            {
                accepted = prompt(host, fp).GetAwaiter().GetResult();
            }
            catch
            {
                accepted = false;
            }
            e.CanTrust = accepted;
            if (accepted) onTrust?.Invoke(fp);
        };
    }

    /// <summary>
    /// Build a <see cref="ConnectionInfo"/> for <paramref name="node"/>. Pure
    /// — no network I/O. Tests assert the resulting port, user, and auth
    /// method count without touching a socket.
    /// </summary>
    public static ConnectionInfo BuildConnectionInfo(
        Node node,
        string? dpapiPassword,
        string? sshDir = null)
    {
        if (string.IsNullOrEmpty(node.SshHost))
            throw new NoCredentialException("SSH node missing host");
        var methods = BuildAuthMethods(node, dpapiPassword, sshDir);
        return new ConnectionInfo(node.SshHost, node.EffectiveSshPort, node.SshUser, methods)
        {
            Timeout = TimeSpan.FromSeconds(15),
        };
    }

    /// <summary>
    /// One-shot connect helper used by invoker/bootstrap. Returns a live
    /// <see cref="SshClient"/>; caller owns disposal.
    /// </summary>
    public static SshClient Connect(
        Node node,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<string>? onTrust = null,
        Func<Guid, string?>? passwordProvider = null)
    {
        var password = node.AuthMethod == AuthMethod.Password
            ? (passwordProvider ?? DefaultPasswordProvider)(node.Id)
            : null;
        var info = BuildConnectionInfo(node, password);
        var client = new SshClient(info);
        WireHostKeyValidation(client, node.SshHost ?? "", node.KnownHostFingerprint, hostKeyPrompt, onTrust);
        client.Connect();
        return client;
    }

    /// <summary>Same shape as <see cref="Connect"/> but returns an SFTP client.</summary>
    public static SftpClient ConnectSftp(
        Node node,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<string>? onTrust = null,
        Func<Guid, string?>? passwordProvider = null)
    {
        var password = node.AuthMethod == AuthMethod.Password
            ? (passwordProvider ?? DefaultPasswordProvider)(node.Id)
            : null;
        var info = BuildConnectionInfo(node, password);
        var client = new SftpClient(info);
        WireHostKeyValidation(client, node.SshHost ?? "", node.KnownHostFingerprint, hostKeyPrompt, onTrust);
        client.Connect();
        return client;
    }

    private static string? DefaultPasswordProvider(Guid nodeId) => DpapiStore.GetPassword(nodeId);
}
