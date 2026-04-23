using System.Runtime.InteropServices;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Bootstrap handshake for a remote SSH host: detect its triple, upload the right sampler
/// via scp.exe, self-check. Mirrors SSHBootstrap.swift on the Mac side.
/// </summary>
public sealed class SshBootstrap
{
    private readonly IProcessRunner _runner;
    private readonly Node _node;
    private readonly string _sshExe;
    private readonly string _scpExe;

    public SshBootstrap(IProcessRunner runner, Node node, string? sshExe = null, string? scpExe = null)
    {
        _runner = runner;
        _node = node;
        _sshExe = sshExe ?? SshSamplerInvoker.DefaultSshPath();
        _scpExe = scpExe ?? DefaultScpPath();
    }

    public enum RemoteOs { Linux, Darwin, Windows, Unknown }

    public sealed record RemoteTriple(RemoteOs Os, string Arch, string Triple, string DeployPath)
    {
        public bool IsWindows => Os == RemoteOs.Windows;
    }

    /// <summary>
    /// Run <c>uname -sm || ver</c> on the remote host — Unix returns <c>uname</c>
    /// output (<c>Linux x86_64</c>), Windows falls through to the DOS <c>ver</c>
    /// output (<c>Microsoft Windows [Version 10.0.22000.1]</c>). We parse both
    /// shapes so the same bootstrap works for Linux/macOS/Windows hosts.
    /// </summary>
    public async Task<RemoteTriple> DetectAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            BuildSsh("uname -sm || ver", TimeSpan.FromSeconds(10)),
            ct).ConfigureAwait(false);
        var blob = (result.StdOut + "\n" + result.StdErr).Trim();
        return ParseUnameOrVer(blob);
    }

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
            // Windows `ver` doesn't expose arch — ARM64 hosts are rare enough we default to amd64
            // and the user can override via the Preferences → Servers → Advanced field (see plan).
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
    /// Upload the local bundled binary for <paramref name="triple"/> to the remote deploy path.
    /// </summary>
    public async Task<bool> DeployAsync(RemoteTriple target, string? bundleRoot = null, CancellationToken ct = default)
    {
        var root = bundleRoot ?? AppContext.BaseDirectory;
        var ext = target.IsWindows ? ".exe" : "";
        var localPath = Path.Combine(root, "Assets", "samplers", target.Triple, $"towertail-sampler{ext}");
        if (!File.Exists(localPath)) return false;

        // scp <local> user@host:<remotePath>  — use forward slashes; OpenSSH accepts them
        // even when the remote is Windows.
        var remotePath = target.DeployPath.Replace('\\', '/');
        var scpArgs = new List<string>
        {
            "-o", "BatchMode=yes",
            localPath,
            $"{_node.UserAtHost}:{remotePath}",
        };
        var result = await _runner.RunAsync(
            new ProcessRequest(_scpExe, scpArgs, Timeout: TimeSpan.FromSeconds(30)), ct).ConfigureAwait(false);
        return result.Ok;
    }

    /// <summary>
    /// Run <c>--self-check</c> on the newly-deployed binary.
    /// </summary>
    public async Task<bool> VerifyAsync(RemoteTriple target, CancellationToken ct = default)
    {
        var cmd = target.IsWindows
            ? $"{target.DeployPath} --self-check"
            : $"{target.DeployPath} --self-check";
        var result = await _runner.RunAsync(BuildSsh(cmd, TimeSpan.FromSeconds(10)), ct).ConfigureAwait(false);
        return result.Ok && result.StdOut.Trim() == "ok";
    }

    private ProcessRequest BuildSsh(string remoteCommand, TimeSpan? timeout = null)
    {
        var args = new List<string>
        {
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=10",
            _node.UserAtHost,
            remoteCommand,
        };
        return new ProcessRequest(_sshExe, args, Timeout: timeout);
    }

    internal static string DefaultScpPath()
    {
        var sys = Environment.GetFolderPath(Environment.SpecialFolder.System);
        var baked = Path.Combine(sys, "OpenSSH", "scp.exe");
        return File.Exists(baked) ? baked : "scp.exe";
    }
}
