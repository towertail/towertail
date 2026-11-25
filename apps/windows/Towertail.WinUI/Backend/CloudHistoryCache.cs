using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>The history routes of the server. <see cref="RemoteClient"/> is the real one.</summary>
public interface IHistorySource
{
    Task<RemoteHistoryResponse> HistoryAsync(Guid nodeId, IReadOnlyList<string> metrics, DateTime from, DateTime to, TimeSpan? step = null, CancellationToken ct = default);
    Task<RemoteDiskHistory> DiskHistoryAsync(Guid nodeId, DateTime from, DateTime to, CancellationToken ct = default);
    Task<RemoteProcessSnapshot> ProcessesAtAsync(Guid nodeId, DateTime ts, ProcSort by, int limit, CancellationToken ct = default);
    Task<RemoteOffenders> OffendersAsync(Guid nodeId, DateTime from, DateTime to, ProcSort by, int limit, int series, CancellationToken ct = default);
    Task<RemoteProcessDetail> ProcessAsync(Guid nodeId, string key, DateTime from, DateTime to, TimeSpan step, CancellationToken ct = default);
}

/// <summary>
/// Keeps the last history answers for a short time, so that a tab switch or a quick cursor move
/// sends no request. Callers that ask for the same entry share one request. A failure is not kept.
/// </summary>
public sealed class CloudHistoryCache
{
    public static readonly TimeSpan Ttl = TimeSpan.FromSeconds(60);
    public static readonly TimeSpan OpenBucketTtl = TimeSpan.FromSeconds(10);
    public const int SnapshotCapacity = 200;

    private readonly IHistorySource _source;
    private readonly TimeProvider _time;
    private readonly object _gate = new();
    private readonly Dictionary<string, (DateTime At, object Value)> _entries = new();
    private readonly Dictionary<string, Task<object>> _inflight = new();
    private readonly Dictionary<string, SnapshotEntry> _snapshots = new();
    private readonly List<string> _snapshotOrder = new();

    private sealed record SnapshotEntry(RemoteProcessSnapshot Value, DateTime At, bool Closed);

    public CloudHistoryCache(IHistorySource source, TimeProvider? time = null)
    {
        _source = source;
        _time = time ?? TimeProvider.System;
    }

    private DateTime Now => _time.GetUtcNow().UtcDateTime;

    /// <summary>The key rounds the window down to the step, so a window that moves by less than a step reuses the entry.</summary>
    public Task<RemoteHistoryResponse> HistoryAsync(
        Guid id, IReadOnlyList<string> metrics, DateTime from, DateTime to, TimeSpan step, CancellationToken ct = default)
        => CachedAsync($"node|{id}|{string.Join(",", metrics)}|{Floor(from, step)}|{Floor(to, step)}|{(long)step.TotalSeconds}",
            () => _source.HistoryAsync(id, metrics, from, to, step), ct);

    public Task<RemoteDiskHistory> DisksAsync(Guid id, DateTime from, DateTime to, CancellationToken ct = default)
        => CachedAsync($"disks|{id}|{Floor(from, TimeSpan.FromSeconds(60))}|{Floor(to, TimeSpan.FromSeconds(60))}",
            () => _source.DiskHistoryAsync(id, from, to), ct);

    public Task<RemoteOffenders> OffendersAsync(Guid id, DateTime from, DateTime to, ProcSort by, CancellationToken ct = default)
        => CachedAsync($"offenders|{id}|{by}|{Floor(from, TimeSpan.FromSeconds(10))}|{Floor(to, TimeSpan.FromSeconds(10))}",
            () => _source.OffendersAsync(id, from, to, by, 10, 60), ct);

    public Task<RemoteProcessDetail> ProcessAsync(
        Guid id, string key, DateTime from, DateTime to, TimeSpan step, CancellationToken ct = default)
        => CachedAsync($"process|{id}|{key}|{Floor(from, step)}|{Floor(to, step)}|{(long)step.TotalSeconds}",
            () => _source.ProcessAsync(id, key, from, to, step), ct);

    /// <summary>The stored snapshot, or null. It sends no request.</summary>
    public RemoteProcessSnapshot? TryGetProcessesAt(Guid id, ProcBucket bucket, ProcSort by)
    {
        var key = SnapshotKey(id, bucket, by);
        lock (_gate)
        {
            if (!_snapshots.TryGetValue(key, out var e)) return null;
            if (!e.Closed && Now - e.At >= OpenBucketTtl)
            {
                _snapshots.Remove(key);
                _snapshotOrder.Remove(key);
                return null;
            }
            Touch(key);
            return e.Value;
        }
    }

    /// <summary>A closed bucket never expires. The open bucket expires after 10 s.</summary>
    public async Task<RemoteProcessSnapshot> ProcessesAtAsync(Guid id, ProcBucket bucket, ProcSort by, CancellationToken ct = default)
    {
        if (TryGetProcessesAt(id, bucket, by) is { } hit) return hit;
        var key = SnapshotKey(id, bucket, by);
        var value = await SharedAsync("snap|" + key,
            () => _source.ProcessesAtAsync(id, bucket.Start, by, bucket.Limit),
            v =>
            {
                var closed = bucket.End < Now;
                _snapshots[key] = new SnapshotEntry(v, Now, closed);
                Touch(key);
                while (_snapshotOrder.Count > SnapshotCapacity)
                {
                    _snapshots.Remove(_snapshotOrder[0]);
                    _snapshotOrder.RemoveAt(0);
                }
            }, ct).ConfigureAwait(false);
        return value;
    }

    private static string SnapshotKey(Guid id, ProcBucket b, ProcSort by)
        => $"{id}|{by}|{b.TierName}|{new DateTimeOffset(b.Start).ToUnixTimeSeconds()}";

    private void Touch(string key)
    {
        _snapshotOrder.Remove(key);
        _snapshotOrder.Add(key);
    }

    private static long Floor(DateTime d, TimeSpan step)
    {
        var s = Math.Max(1, (long)step.TotalSeconds);
        return new DateTimeOffset(d.ToUniversalTime()).ToUnixTimeSeconds() / s * s;
    }

    private async Task<T> CachedAsync<T>(string key, Func<Task<T>> fetch, CancellationToken ct) where T : class
    {
        lock (_gate)
        {
            if (_entries.TryGetValue(key, out var e) && Now - e.At < Ttl && e.Value is T hit) return hit;
        }
        return await SharedAsync(key, fetch, v =>
        {
            var t = Now;
            foreach (var old in _entries.Where(x => t - x.Value.At >= Ttl).Select(x => x.Key).ToList()) _entries.Remove(old);
            _entries[key] = (t, v);
        }, ct).ConfigureAwait(false);
    }

    /// <summary>Joins the running request of the key, or starts one. A cancelled caller leaves the request running.</summary>
    private async Task<T> SharedAsync<T>(string key, Func<Task<T>> fetch, Action<T> store, CancellationToken ct) where T : class
    {
        Task<object> task;
        lock (_gate)
        {
            if (!_inflight.TryGetValue(key, out task!))
            {
                task = Task.Run(() => RunAsync(key, fetch, store));
                _inflight[key] = task;
            }
        }
        return (T)await task.WaitAsync(ct).ConfigureAwait(false);
    }

    private async Task<object> RunAsync<T>(string key, Func<Task<T>> fetch, Action<T> store) where T : class
    {
        try
        {
            var value = await fetch().ConfigureAwait(false);
            lock (_gate) store(value);
            return value;
        }
        finally
        {
            lock (_gate) _inflight.Remove(key);
        }
    }
}
