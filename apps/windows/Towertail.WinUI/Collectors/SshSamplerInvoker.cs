using Renci.SshNet;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs a remote <c>towertail-sampler</c> over SSH via SSH.NET. Replaces the
/// earlier <c>ssh.exe</c> shell-out so password auth + host-key pinning work
/// natively on Windows without OpenSSH client assumptions.
/// </summary>
public sealed class SshSamplerInvoker : ISamplerInvoker
{
    private readonly Node _node;
    private readonly string _remotePath;
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;

    public SshSamplerInvoker(
        Node node,
        string? remotePath = null,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null)
    {
        _node = node;
        _remotePath = remotePath ?? "~/.towertail/towertail-sampler";
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
    }

    public Task<Sample> RunOnceAsync(CancellationToken ct = default)
        => RunOnThreadPool(() =>
        {
            using var client = Connect();
            var output = client.RunCommand($"{_remotePath} --once");
            if (output.ExitStatus != 0)
                throw new InvalidOperationException($"sampler exit {output.ExitStatus}: {output.Error.Trim()}");
            return SampleCodec.Decode(output.Result);
        }, ct);

    public async IAsyncEnumerable<Sample> StreamAsync(
        TimeSpan interval,
        [global::System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        // SSH.NET's ShellStream lets us keep one session open; simpler here is
        // to poll --once on the outer cadence the caller already drives. The
        // worker loop in RealCollector already paces per-node, so a streaming
        // --interval subprocess isn't worth the extra plumbing.
        var seconds = Math.Max(1, (int)interval.TotalSeconds);
        while (!ct.IsCancellationRequested)
        {
            Sample? sample = null;
            try { sample = await RunOnceAsync(ct).ConfigureAwait(false); }
            catch { /* transport error — outer loop will back off */ }
            if (sample != null) yield return sample;
            try { await Task.Delay(TimeSpan.FromSeconds(seconds), ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { yield break; }
        }
    }

    public Task<string> VersionAsync(CancellationToken ct = default)
        => RunOnThreadPool(() =>
        {
            using var client = Connect();
            var output = client.RunCommand($"{_remotePath} --version");
            return output.Result.Trim();
        }, ct);

    public Task<bool> SelfCheckAsync(CancellationToken ct = default)
        => RunOnThreadPool(() =>
        {
            using var client = Connect();
            var output = client.RunCommand($"{_remotePath} --self-check");
            return output.ExitStatus == 0 && output.Result.Trim() == "ok";
        }, ct);

    private SshClient Connect() => SshConnectionFactory.Connect(
        _node,
        hostKeyPrompt: _hostKeyPrompt,
        onTrust: _onTrust is null ? null : fp => _onTrust(_node.Id, fp));

    // SSH.NET is synchronous-first. Wrap each op in Task.Run so callers on the
    // UI thread don't block the dispatcher for the duration of a connect.
    private static Task<T> RunOnThreadPool<T>(Func<T> f, CancellationToken ct)
        => Task.Run(f, ct);
}
