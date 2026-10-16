using System.Runtime.CompilerServices;
using System.Text.Json;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs a disk scan on a node and streams its events. The last event is
/// <see cref="DiskScanEvent.Result"/>, <see cref="DiskScanEvent.Error"/> or
/// <see cref="DiskScanEvent.Cancelled"/>. Cancel the token to stop the scan: the stream then
/// ends with <see cref="DiskScanEvent.Cancelled"/> and does not throw.
/// </summary>
public interface IDiskScanner
{
    IAsyncEnumerable<DiskScanEvent> ScanAsync(Node node, DiskScanRequest request, CancellationToken ct = default);
}

/// <summary>Picks the scanner for a node, the same pattern as <see cref="SamplerInvokerFactory"/>.</summary>
public sealed class DiskScannerFactory
{
    private readonly IProcessRunner _runner;
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;
    private readonly SamplerUpdateCoordinator? _updater;
    private readonly RemoteClient? _remote;
    private readonly IDiskScanUpdates? _updates;

    public DiskScannerFactory(
        IProcessRunner? runner = null,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null,
        SamplerUpdateCoordinator? updater = null,
        RemoteClient? remote = null,
        IDiskScanUpdates? updates = null)
    {
        _runner = runner ?? new DefaultProcessRunner();
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
        _updater = updater;
        _remote = remote;
        _updates = updates;
    }

    /// <summary>True when the scans go through the server.</summary>
    public bool IsRemote => _remote != null;

    public IDiskScanner Create(Node node)
    {
        if (_remote != null) return new RemoteDiskScanner(_remote, _updates);
        return node.Kind switch
        {
            NodeKind.Local => new LocalDiskScanner(_runner),
            NodeKind.Ssh => new SshDiskScanner(_hostKeyPrompt, _onTrust, _updater),
            _ => throw new NotSupportedException($"unknown node kind {node.Kind}"),
        };
    }
}

/// <summary>A source of scan lines: a local process or an SSH channel.</summary>
public interface IScanChannel : IAsyncDisposable
{
    /// <summary>The next stdout line, or null at EOF.</summary>
    Task<string?> ReadLineAsync();

    /// <summary>Close stdin. The sampler then writes <c>cancelled</c> and exits.</summary>
    void Stop();

    /// <summary>A short text about the exit, for an error message. Call after EOF.</summary>
    Task<string> ExitInfoAsync();
}

/// <summary>
/// Turns the lines of an <see cref="IScanChannel"/> into events. It adds the watchdog
/// (no line for a time fails the scan) and the cancel grace (after a stop, wait a short
/// time for the <c>cancelled</c> line).
/// </summary>
public sealed class DiskScanStream
{
    public static readonly TimeSpan DefaultCancelGrace = TimeSpan.FromSeconds(3);

    private readonly IScanChannel _channel;
    private readonly TimeSpan? _watchdog;
    private readonly TimeSpan _cancelGrace;

    public DiskScanStream(IScanChannel channel, TimeSpan? watchdog = null, TimeSpan? cancelGrace = null)
    {
        _channel = channel;
        _watchdog = watchdog;
        _cancelGrace = cancelGrace ?? DefaultCancelGrace;
    }

    private enum StepKind { Line, Eof, Watchdog, Grace }
    private readonly record struct Step(StepKind Kind, string? Line = null);

    public async IAsyncEnumerable<DiskScanEvent> ReadAsync([EnumeratorCancellation] CancellationToken ct = default)
    {
        var stopped = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        using var reg = ct.Register(() => { _channel.Stop(); stopped.TrySetResult(); });
        DateTime? graceEnd = null;

        while (true)
        {
            var step = await NextAsync(stopped, () => graceEnd, v => graceEnd = v).ConfigureAwait(false);
            switch (step.Kind)
            {
                case StepKind.Line:
                    if (TryDecode(step.Line!) is not { } ev) continue;
                    yield return ev;
                    if (ev.IsFinal) yield break;
                    break;
                case StepKind.Eof:
                    if (graceEnd != null) { yield return new DiskScanEvent.Cancelled(); yield break; }
                    var info = await SafeExitInfoAsync().ConfigureAwait(false);
                    yield return new DiskScanEvent.Error($"The scan ended without a result ({info}).");
                    yield break;
                case StepKind.Grace:
                    yield return new DiskScanEvent.Cancelled();
                    yield break;
                case StepKind.Watchdog:
                    _channel.Stop();
                    yield return new DiskScanEvent.Error(
                        $"The scan sent no output for {_watchdog!.Value.TotalSeconds:0} s. It was stopped.");
                    yield break;
            }
        }
    }

    private async Task<Step> NextAsync(TaskCompletionSource stopped, Func<DateTime?> graceEnd, Action<DateTime> setGraceEnd)
    {
        var read = _channel.ReadLineAsync();
        var lineStart = DateTime.UtcNow;
        while (true)
        {
            var timeout = Timeout.InfiniteTimeSpan;
            var isGrace = false;
            if (graceEnd() is DateTime ge)
            {
                timeout = ge - DateTime.UtcNow;
                isGrace = true;
                if (timeout < TimeSpan.Zero) timeout = TimeSpan.Zero;
            }
            else if (_watchdog is TimeSpan w)
            {
                timeout = w - (DateTime.UtcNow - lineStart);
                if (timeout < TimeSpan.Zero) timeout = TimeSpan.Zero;
            }

            using var timerCts = new CancellationTokenSource();
            var timer = Task.Delay(timeout, timerCts.Token);
            var waits = new List<Task> { read, timer };
            if (graceEnd() is null) waits.Add(stopped.Task);
            var winner = await Task.WhenAny(waits).ConfigureAwait(false);
            timerCts.Cancel();

            if (winner == read)
            {
                string? line;
                try { line = await read.ConfigureAwait(false); }
                catch { line = null; }
                return line is null ? new Step(StepKind.Eof) : new Step(StepKind.Line, line);
            }
            if (winner == stopped.Task)
            {
                setGraceEnd(DateTime.UtcNow + _cancelGrace);
                continue;
            }
            return new Step(isGrace ? StepKind.Grace : StepKind.Watchdog);
        }
    }

    private static DiskScanEvent? TryDecode(string line)
    {
        if (string.IsNullOrWhiteSpace(line)) return null;
        try { return DiskScanCodec.DecodeLine(line); }
        catch (JsonException) { return null; }
    }

    private async Task<string> SafeExitInfoAsync()
    {
        try { return await _channel.ExitInfoAsync().ConfigureAwait(false); }
        catch (Exception ex) { return ex.Message; }
    }
}
