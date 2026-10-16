using System.Runtime.CompilerServices;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs <c>towertail-sampler.exe scan</c> from the bundle on this PC. Stdin stays open
/// while the scan runs; cancel closes it. Windows has no Full Disk Access: folders that the
/// user cannot read come back in <c>no_access</c>.
/// </summary>
public sealed class LocalDiskScanner : IDiskScanner
{
    private readonly IProcessRunner _runner;
    private readonly string _executablePath;
    private readonly TimeSpan? _watchdog;
    private readonly TimeSpan? _cancelGrace;

    public LocalDiskScanner(IProcessRunner runner, string? explicitPath = null,
        TimeSpan? watchdog = null, TimeSpan? cancelGrace = null)
    {
        _runner = runner;
        _executablePath = explicitPath ?? LocalSamplerInvoker.ResolveBundledSampler();
        _watchdog = watchdog;
        _cancelGrace = cancelGrace;
    }

    public async IAsyncEnumerable<DiskScanEvent> ScanAsync(
        Node node, DiskScanRequest request, [EnumeratorCancellation] CancellationToken ct = default)
    {
        IStreamingProcess? proc = null;
        string? startError = null;
        try
        {
            proc = await _runner.StartStreamingAsync(
                new ProcessRequest(_executablePath, request.Arguments()), CancellationToken.None).ConfigureAwait(false);
        }
        catch (Exception ex) { startError = ex.Message; }
        if (proc is null)
        {
            yield return new DiskScanEvent.Error($"Could not start the sampler: {startError}");
            yield break;
        }

        await using var channel = new ProcessScanChannel(proc);
        await foreach (var ev in new DiskScanStream(channel, _watchdog, _cancelGrace).ReadAsync(ct).ConfigureAwait(false))
            yield return ev;
    }

    private sealed class ProcessScanChannel : IScanChannel
    {
        private readonly IStreamingProcess _proc;
        public ProcessScanChannel(IStreamingProcess proc) { _proc = proc; }

        public Task<string?> ReadLineAsync() => _proc.StdOut.ReadLineAsync();
        public void Stop() => _proc.CloseStdIn();

        public async Task<string> ExitInfoAsync()
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(2));
            var wait = _proc.WaitAsync();
            var done = await Task.WhenAny(wait, Task.Delay(Timeout.Infinite, cts.Token)).ConfigureAwait(false);
            var code = done == wait ? $"exit {await wait.ConfigureAwait(false)}" : "no exit code";
            var err = _proc.StdErrText.Trim();
            return string.IsNullOrEmpty(err) ? code : $"{code}: {err}";
        }

        public ValueTask DisposeAsync() => _proc.DisposeAsync();
    }
}
