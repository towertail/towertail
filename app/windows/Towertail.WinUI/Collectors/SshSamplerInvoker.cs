using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs a remote <c>towertail-sampler</c> over Windows OpenSSH. Uses <c>ssh.exe</c> from
/// <c>%SystemRoot%\System32\OpenSSH</c> (available out of the box since Windows 10 1809) —
/// which honors <c>~/.ssh/config</c>, ssh-agent, and known_hosts exactly like the Mac client.
/// </summary>
public sealed class SshSamplerInvoker : ISamplerInvoker
{
    private readonly IProcessRunner _runner;
    private readonly Node _node;
    private readonly string _sshExe;
    private readonly string _remotePath;

    public SshSamplerInvoker(IProcessRunner runner, Node node, string? sshExe = null, string? remotePath = null)
    {
        _runner = runner;
        _node = node;
        _sshExe = sshExe ?? DefaultSshPath();
        // Default remote install path. Unix hosts: ~/.towertail/towertail-sampler.
        // Windows hosts: bootstrap rewrites this to the %USERPROFILE% variant before first use.
        _remotePath = remotePath ?? "~/.towertail/towertail-sampler";
    }

    public async Task<Sample> RunOnceAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            BuildSshRequest($"{_remotePath} --once", TimeSpan.FromSeconds(15)),
            ct).ConfigureAwait(false);
        if (!result.Ok)
            throw new InvalidOperationException($"ssh exit {result.ExitCode}: {result.StdErr.Trim()}");
        return SampleCodec.Decode(result.StdOut);
    }

    public async IAsyncEnumerable<Sample> StreamAsync(
        TimeSpan interval,
        [global::System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        var seconds = Math.Max(1, (int)interval.TotalSeconds);
        await using var proc = await _runner.StartStreamingAsync(
            BuildSshRequest($"{_remotePath} --interval {seconds}s"),
            ct).ConfigureAwait(false);
        while (!ct.IsCancellationRequested)
        {
            var line = await proc.StdOut.ReadLineAsync(ct).ConfigureAwait(false);
            if (line is null) yield break;
            if (string.IsNullOrWhiteSpace(line)) continue;
            Sample? sample = null;
            try { sample = SampleCodec.Decode(line); }
            catch { }
            if (sample != null) yield return sample;
        }
    }

    public async Task<string> VersionAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            BuildSshRequest($"{_remotePath} --version", TimeSpan.FromSeconds(5)),
            ct).ConfigureAwait(false);
        return result.StdOut.Trim();
    }

    public async Task<bool> SelfCheckAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            BuildSshRequest($"{_remotePath} --self-check", TimeSpan.FromSeconds(10)),
            ct).ConfigureAwait(false);
        return result.Ok && result.StdOut.Trim() == "ok";
    }

    private ProcessRequest BuildSshRequest(string remoteCommand, TimeSpan? timeout = null)
    {
        var target = _node.UserAtHost;
        var args = new List<string>
        {
            "-o", "ControlMaster=auto",
            "-o", "ControlPersist=60",
            "-o", "ControlPath=~/.ssh/towertail-%r@%h:%p",
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=10",
            target,
            remoteCommand,
        };
        return new ProcessRequest(_sshExe, args, Timeout: timeout);
    }

    internal static string DefaultSshPath()
    {
        var sys = Environment.GetFolderPath(Environment.SpecialFolder.System);
        var baked = Path.Combine(sys, "OpenSSH", "ssh.exe");
        return File.Exists(baked) ? baked : "ssh.exe";
    }
}
