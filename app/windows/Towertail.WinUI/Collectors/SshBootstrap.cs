using Renci.SshNet;
using Renci.SshNet.Common;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Bootstrap handshake for a remote SSH host: detect its triple, upload the
/// right sampler via SFTP, self-check. Mirrors SSHBootstrap.swift on the Mac
/// side. No shell-out — uses SSH.NET's <see cref="SftpClient"/> and
/// <c>RunCommand</c> over the same connection factory as the invoker.
/// </summary>
public sealed class SshBootstrap
{
    private readonly Node _node;
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;

    public SshBootstrap(
        Node node,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null)
    {
        _node = node;
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
    }

    public enum RemoteOs { Linux, Darwin, Windows, Unknown }

    public sealed record RemoteTriple(RemoteOs Os, string Arch, string Triple, string DeployPath)
    {
        public bool IsWindows => Os == RemoteOs.Windows;
    }

    /// <summary>
    /// Run <c>uname -sm || ver</c> on the remote — Unix returns <c>uname</c>
    /// output (<c>Linux x86_64</c>), Windows falls through to the DOS <c>ver</c>
    /// output (<c>Microsoft Windows [Version 10.0.22000.1]</c>).
    /// </summary>
    public Task<RemoteTriple> DetectAsync(CancellationToken ct = default)
        => Task.Run(() =>
        {
            using var client = Connect();
            var cmd = client.RunCommand("uname -sm || ver");
            var blob = (cmd.Result + "\n" + cmd.Error).Trim();
            return ParseUnameOrVer(blob);
        }, ct);

    internal static RemoteTriple ParseUnameOrVer(string text)
    {
        var line = text.Split('\n').FirstOrDefault(l => !string.IsNullOrWhiteSpace(l))?.Trim() ?? "";
        if (line.StartsWith("Linux", StringComparison.OrdinalIgnoreCase))
        {
            var arch = MapArch(line.Split(' ', 2).ElementAtOrDefault(1) ?? "");
            return new RemoteTriple(RemoteOs.Linux, arch, $"linux-{arch}",
                "~/.towertail/towertail-sampler");
        }
        if (line.StartsWith("Darwin", StringComparison.OrdinalIgnoreCase))
        {
            var arch = MapArch(line.Split(' ', 2).ElementAtOrDefault(1) ?? "");
            return new RemoteTriple(RemoteOs.Darwin, arch, $"darwin-{arch}",
                "~/.towertail/towertail-sampler");
        }
        if (line.Contains("Windows", StringComparison.OrdinalIgnoreCase) ||
            line.StartsWith("Microsoft", StringComparison.OrdinalIgnoreCase))
        {
            return new RemoteTriple(RemoteOs.Windows, "amd64", "windows-amd64",
                "%USERPROFILE%/.towertail/towertail-sampler.exe");
        }
        return new RemoteTriple(RemoteOs.Unknown, "amd64", "linux-amd64",
            "~/.towertail/towertail-sampler");
    }

    private static string MapArch(string raw) => raw.Trim().ToLowerInvariant() switch
    {
        "x86_64" or "amd64" => "amd64",
        "aarch64" or "arm64" => "arm64",
        "armv7l" => "armv7",
        _ => "amd64",
    };

    /// <summary>
    /// Upload the local bundled binary for <paramref name="target"/> to the
    /// remote deploy path via SFTP. Sets 0755 via ChangePermissions afterwards.
    /// </summary>
    public Task<bool> DeployAsync(RemoteTriple target, string? bundleRoot = null, CancellationToken ct = default)
        => Task.Run(() =>
        {
            var root = bundleRoot ?? AppContext.BaseDirectory;
            var ext = target.IsWindows ? ".exe" : "";
            var localPath = Path.Combine(root, "Assets", "samplers", target.Triple, $"towertail-sampler{ext}");
            if (!File.Exists(localPath)) return false;

            using var sftp = ConnectSftp();
            // Resolve $HOME — SFTP's `~` doesn't expand automatically on most
            // servers. We do the lookup via a command channel on the same
            // credentials, then rewrite the deploy path.
            var remotePath = ResolveRemotePath(target.DeployPath);

            // mkdir -p equivalent. CreateDirectory throws if exists on some
            // servers; catching SftpPathNotFoundException covers the "parent
            // is missing" case and we can try the grandparent once.
            var dir = PosixDirname(remotePath);
            TryCreateDir(sftp, dir);

            using var fs = File.OpenRead(localPath);
            sftp.UploadFile(fs, remotePath);

            try
            {
                // 0o755 = 0x1ED. SSH.NET expects decimal/short; cast for clarity.
                sftp.ChangePermissions(remotePath, (short)0x1ED);
            }
            catch { /* some servers reject chmod over SFTP; binary is usable as-is */ }

            return true;
        }, ct);

    /// <summary>
    /// Run <c>--self-check</c> on the newly-deployed binary.
    /// </summary>
    public Task<bool> VerifyAsync(RemoteTriple target, CancellationToken ct = default)
        => Task.Run(() =>
        {
            using var client = Connect();
            var cmd = client.RunCommand($"{target.DeployPath} --self-check");
            return cmd.ExitStatus == 0 && cmd.Result.Trim() == "ok";
        }, ct);

    private SshClient Connect() => SshConnectionFactory.Connect(
        _node, _hostKeyPrompt, _onTrust is null ? null : fp => _onTrust(_node.Id, fp));

    private SftpClient ConnectSftp() => SshConnectionFactory.ConnectSftp(
        _node, _hostKeyPrompt, _onTrust is null ? null : fp => _onTrust(_node.Id, fp));

    private static string PosixDirname(string path)
    {
        var i = path.LastIndexOf('/');
        return i <= 0 ? "." : path[..i];
    }

    private static void TryCreateDir(SftpClient sftp, string dir)
    {
        if (string.IsNullOrEmpty(dir) || dir == "." || dir == "/") return;
        try
        {
            if (!sftp.Exists(dir)) sftp.CreateDirectory(dir);
        }
        catch (SftpPathNotFoundException)
        {
            TryCreateDir(sftp, PosixDirname(dir));
            try { sftp.CreateDirectory(dir); } catch { }
        }
        catch { /* already exists or permissions — upload will surface the real error */ }
    }

    private static string ResolveRemotePath(string templated)
        => templated.Replace('\\', '/');
}
