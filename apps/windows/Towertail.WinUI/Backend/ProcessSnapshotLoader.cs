using System.Diagnostics;
using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>One row of the process table of a cloud host. Raw rows have a PID. Rollup rows have a peak.</summary>
public sealed record ProcessSnapshotRow(
    ProcessRef Ref, string? User, double CpuPct, double? CpuPctMax, long Rss, double? IoBps, int? Threads, DateTime? Started);

/// <summary>
/// The process snapshot of a cloud host at the cursor. It waits for the cursor to rest, keeps the
/// old rows while it loads, and gets the buckets around the new one. Views marshal the property events.
/// </summary>
public sealed partial class ProcessSnapshotLoader : ObservableObject
{
    private readonly Guid _host;
    private readonly string _hostLabel;
    private readonly CloudHistoryCache _cache;
    private readonly TimeSpan _debounce;
    private readonly TimeSpan _loadingDelay;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly object _gate = new();
    private CancellationTokenSource? _run;
    private CancellationTokenSource? _prefetch;
    private Target? _target;

    private sealed record Target(ProcBucket Bucket, ProcSort By);

    [ObservableProperty] private RemoteProcessSnapshot? _snapshot;
    /// <summary>The bucket of <see cref="Snapshot"/>.</summary>
    [ObservableProperty] private ProcBucket? _bucket;
    [ObservableProperty] private bool _showsLoading;
    [ObservableProperty] private string? _error;
    /// <summary>The request path with its size and time, or "cache hit".</summary>
    [ObservableProperty] private string? _lastRequest;

    public ProcessSnapshotLoader(
        Guid host, CloudHistoryCache cache, TimeSpan? debounce = null, TimeSpan? loadingDelay = null,
        Func<TimeSpan, CancellationToken, Task>? delay = null, string? hostLabel = null)
    {
        _host = host;
        _hostLabel = hostLabel ?? host.ToString();
        _cache = cache;
        _debounce = debounce ?? TimeSpan.FromMilliseconds(120);
        _loadingDelay = loadingDelay ?? TimeSpan.FromMilliseconds(80);
        _delay = delay ?? ((span, ct) => Task.Delay(span, ct));
    }

    /// <summary>Asks for the snapshot of the bucket of <paramref name="t"/>. Many calls in a row send one request.</summary>
    public void Request(DateTime t, ProcSort by, DateTime now)
    {
        var next = new Target(ProcBucket.Of(t, now), by);
        CancellationTokenSource run;
        lock (_gate)
        {
            if (next == _target) return;
            _target = next;
            _run?.Cancel();
            _prefetch?.Cancel();
            run = _run = new CancellationTokenSource();
        }
        _ = Task.Run(() => RunAsync(next, now, run.Token));
    }

    public void Cancel()
    {
        lock (_gate)
        {
            _run?.Cancel();
            _prefetch?.Cancel();
            _run = _prefetch = null;
            _target = null;
        }
        ShowsLoading = false;
    }

    private async Task RunAsync(Target t, DateTime now, CancellationToken ct)
    {
        var path = $"GET /v1/nodes/{_hostLabel}/processes/at?ts={t.Bucket.Start:yyyy-MM-dd'T'HH:mm:ss'Z'}&by={RemoteClient.SortName(t.By)}&limit={t.Bucket.Limit}";
        if (_cache.TryGetProcessesAt(_host, t.Bucket, t.By) is { } hit)
        {
            if (ct.IsCancellationRequested) return;
            Show(hit, t.Bucket, "cache hit · " + path);
            ShowsLoading = false;
            StartPrefetch(t, now);
            return;
        }
        try { await _delay(_debounce, ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { return; }

        var started = Stopwatch.GetTimestamp();
        using var loading = CancellationTokenSource.CreateLinkedTokenSource(ct);
        _ = ShowLoadingAsync(loading.Token);
        try
        {
            var snap = await _cache.ProcessesAtAsync(_host, t.Bucket, t.By, ct).ConfigureAwait(false);
            if (ct.IsCancellationRequested) return;
            var ms = (int)Stopwatch.GetElapsedTime(started).TotalMilliseconds;
            Show(snap, t.Bucket, $"{path} · {snap.Bytes / 1024.0:0.0} KB · {ms} ms");
            ShowsLoading = false;
            StartPrefetch(t, now);
        }
        catch (OperationCanceledException) { }
        catch (Exception ex)
        {
            if (ct.IsCancellationRequested) return;
            Error = CloudHistoryError.Message(ex);
            ShowsLoading = false;
            lock (_gate) _target = null;
        }
        finally { loading.Cancel(); }
    }

    private async Task ShowLoadingAsync(CancellationToken ct)
    {
        try { await _delay(_loadingDelay, ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { return; }
        if (!ct.IsCancellationRequested) ShowsLoading = true;
    }

    private void Show(RemoteProcessSnapshot snap, ProcBucket bucket, string request)
    {
        Snapshot = snap;
        Bucket = bucket;
        Error = null;
        LastRequest = request;
    }

    /// <summary>Two buckets on each side, none in the future.</summary>
    private void StartPrefetch(Target t, DateTime now)
    {
        var targets = new[] { -2, -1, 1, 2 }.Select(n => t.Bucket.Shifted(n)).Where(b => b.Start <= now).ToList();
        CancellationTokenSource cts;
        lock (_gate)
        {
            _prefetch?.Cancel();
            cts = _prefetch = new CancellationTokenSource();
        }
        _ = Task.Run(async () =>
        {
            foreach (var b in targets)
            {
                if (cts.IsCancellationRequested) return;
                try { await _cache.ProcessesAtAsync(_host, b, t.By, cts.Token).ConfigureAwait(false); }
                catch (Exception) { }
            }
        });
    }

    /// <summary>The rows of the snapshot.</summary>
    public IReadOnlyList<ProcessSnapshotRow> Rows => (Snapshot?.Processes ?? new()).Select(p => new ProcessSnapshotRow(
        new ProcessRef(p.Key, p.Pid, p.Name, p.Cmd), p.User, p.CpuPct, p.CpuPctMax, (long)Math.Min(p.Rss, long.MaxValue),
        p.IoBps, p.Threads, p.StartTs)).ToList();

    /// <summary>"14:02:11 · now", "minute Mar 3 14:02 · 3m ago" or "hour Mar 3 14:00 · 5h ago".</summary>
    public string WhenLabel(DateTime now)
    {
        if (Snapshot is not { } snap || Bucket is not { } bucket) return "no snapshot";
        var t = snap.Ts.ToLocalTime();
        var age = now - snap.Ts;
        var ago = age.TotalSeconds < 15 ? "now" : ShortDuration.Text(age) + " ago";
        return bucket.Tier switch
        {
            ProcBucketTier.Raw => t.ToString("HH:mm:ss") + " · " + ago,
            ProcBucketTier.Minute => "minute " + t.ToString("MMM d HH:mm") + " · " + ago,
            _ => "hour " + t.ToString("MMM d HH:mm") + " · " + ago,
        };
    }

    /// <summary>The text at the left of the footer.</summary>
    public string? Note
    {
        get
        {
            if (Snapshot is not { } snap || Bucket is not { } bucket) return null;
            return bucket.Tier == ProcBucketTier.Raw && snap.Total is int total
                ? $"{snap.Processes.Count} of {total} processes · {bucket.Label}"
                : bucket.Label;
        }
    }
}
