using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.Tests.Helpers;

/// <summary>A hand-set clock. Time moves only when the test says so.</summary>
public sealed class StepClock : TimeProvider
{
    private DateTimeOffset _now;

    public StepClock(DateTime utc) => _now = new DateTimeOffset(utc, TimeSpan.Zero);

    public override DateTimeOffset GetUtcNow() => _now;

    public void Advance(TimeSpan span) => _now += span;
}

/// <summary>A history source that counts calls. A call can wait for <see cref="Gate"/>.</summary>
public sealed class FakeHistorySource : IHistorySource
{
    private int _history, _disks, _snapshots, _offenders, _process;

    public int HistoryCalls => _history;
    public int DiskCalls => _disks;
    public int SnapshotCalls => _snapshots;
    public int OffenderCalls => _offenders;
    public int ProcessCalls => _process;

    public List<DateTime> SnapshotStarts { get; } = new();
    public TaskCompletionSource? Gate { get; set; }
    public Exception? Fail { get; set; }
    public Dictionary<string, List<RemoteHistoryPoint>> Series { get; set; } = new();
    public RemoteProcessDetail? Detail { get; set; }

    private async Task WaitAsync(CancellationToken ct)
    {
        if (Gate is { } g) await g.Task.WaitAsync(ct).ConfigureAwait(false);
        if (Fail is { } f) throw f;
    }

    public async Task<RemoteHistoryResponse> HistoryAsync(Guid nodeId, IReadOnlyList<string> metrics, DateTime from, DateTime to, TimeSpan? step = null, CancellationToken ct = default)
    {
        Interlocked.Increment(ref _history);
        await WaitAsync(ct);
        return new RemoteHistoryResponse { NodeId = nodeId, From = from, To = to, Series = Series };
    }

    public async Task<RemoteDiskHistory> DiskHistoryAsync(Guid nodeId, DateTime from, DateTime to, CancellationToken ct = default)
    {
        Interlocked.Increment(ref _disks);
        await WaitAsync(ct);
        return new RemoteDiskHistory();
    }

    public async Task<RemoteProcessSnapshot> ProcessesAtAsync(Guid nodeId, DateTime ts, ProcSort by, int limit, CancellationToken ct = default)
    {
        Interlocked.Increment(ref _snapshots);
        lock (SnapshotStarts) SnapshotStarts.Add(ts);
        await WaitAsync(ct);
        return new RemoteProcessSnapshot { NodeId = nodeId, Ts = ts };
    }

    public async Task<RemoteOffenders> OffendersAsync(Guid nodeId, DateTime from, DateTime to, ProcSort by, int limit, int series, CancellationToken ct = default)
    {
        Interlocked.Increment(ref _offenders);
        await WaitAsync(ct);
        return new RemoteOffenders { NodeId = nodeId, From = from, To = to };
    }

    public async Task<RemoteProcessDetail> ProcessAsync(Guid nodeId, string key, DateTime from, DateTime to, TimeSpan step, CancellationToken ct = default)
    {
        Interlocked.Increment(ref _process);
        await WaitAsync(ct);
        return Detail ?? new RemoteProcessDetail { NodeId = nodeId, Key = key, From = from, To = to };
    }
}
