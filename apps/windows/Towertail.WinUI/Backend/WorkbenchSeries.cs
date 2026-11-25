using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>
/// The REST series of a cloud host for the window of the workbench. They stay out of the live
/// series, so a 30 d window fits. Units are the units of the charts: percent for CPU, memory and
/// disk, MB/s for the network and the disk I/O. PID, file and inode use stay ratios.
/// </summary>
public sealed class WorkbenchSeries
{
    private static readonly string[] RailMetrics = { "cpu", "mem", "load", "disk", "net_rx", "net_tx", "disk_read", "disk_write" };
    private static readonly string[] HealthMetrics = { "procs", "zombies", "pids", "files", "inodes" };

    private readonly CloudHistoryCache _cache;
    private readonly object _gate = new();
    private Dictionary<string, IReadOnlyList<TimedValue>> _series = new();
    private WindowKey? _key;
    private readonly HashSet<HostTab> _loaded = new();

    private sealed record WindowKey(Guid Host, DateTime From, DateTime To, TimeSpan Step);

    public WorkbenchSeries(CloudHistoryCache cache) => _cache = cache;

    public RemoteDiskHistory? Disks { get; private set; }
    public bool Loading { get; private set; }
    public string? Error { get; private set; }

    /// <summary>The plan limit moved the start of the window.</summary>
    public bool Clamped { get; private set; }

    /// <summary>Raised after a load ends. It runs on a pool thread.</summary>
    public event Action? Changed;

    /// <summary>The names for one tab. The server takes 16 at most. Mac hosts have <c>mem_pressure</c>. Linux hosts have PSI.</summary>
    public static IReadOnlyList<string> Metrics(HostTab tab, bool linux)
    {
        var pressure = linux ? new[] { "psi_mem", "psi_mem_full", "psi_io_full" } : new[] { "mem_pressure" };
        return tab switch
        {
            HostTab.Overview => RailMetrics.Concat(HealthMetrics).Concat(pressure).ToList(),
            HostTab.Health => new[] { "cpu", "mem", "disk" }.Concat(HealthMetrics).Concat(pressure).ToList(),
            HostTab.Disks => new[] { "disk", "inodes", "disk_read", "disk_write" },
            _ => Array.Empty<string>(),
        };
    }

    /// <summary>Loads the series of the tab. A tab loads once for each host and window.</summary>
    public async Task LoadAsync(
        Guid host, HostTab tab, (DateTime From, DateTime To) window, TimeSpan step, bool linux, CancellationToken ct = default)
    {
        var next = new WindowKey(host, window.From, window.To, step);
        lock (_gate)
        {
            if (_key != next)
            {
                // The old points stay until the new ones arrive, but not for another host.
                if (_key?.Host != host)
                {
                    _series = new();
                    Disks = null;
                }
                _key = next;
                _loaded.Clear();
                Clamped = false;
            }
            if (_loaded.Contains(tab)) return;
        }
        var metrics = Metrics(tab, linux);
        if (metrics.Count == 0)
        {
            lock (_gate) _loaded.Add(tab);
            return;
        }
        Loading = true;
        Error = null;
        try
        {
            var history = _cache.HistoryAsync(host, metrics, window.From, window.To, step, ct);
            var mounts = tab == HostTab.Disks ? _cache.DisksAsync(host, window.From, window.To, ct) : null;
            var h = await history.ConfigureAwait(false);
            var d = mounts is null ? null : await mounts.ConfigureAwait(false);
            lock (_gate)
            {
                if (_key != next) return;
                var merged = new Dictionary<string, IReadOnlyList<TimedValue>>(_series);
                foreach (var (name, points) in h.Series)
                    merged[name] = points.Select(p => new TimedValue(p.Ts, Convert(name, p.Value))).ToList();
                _series = merged;
                if (d is not null) Disks = d;
                Clamped |= h.Clamped == true;
                _loaded.Add(tab);
            }
        }
        catch (OperationCanceledException) { return; }
        catch (Exception ex)
        {
            lock (_gate) if (_key != next) return;
            Error = CloudHistoryError.Message(ex);
        }
        finally { Loading = false; }
        Changed?.Invoke();
    }

    /// <summary>The REST points of a metric, then the live points after the last one.</summary>
    public IReadOnlyList<TimedValue> Points(string metric, MetricSeries? live)
    {
        IReadOnlyList<TimedValue> rest;
        lock (_gate) rest = _series.GetValueOrDefault(metric) ?? Array.Empty<TimedValue>();
        return Append(rest, live);
    }

    public static IReadOnlyList<TimedValue> Append(IReadOnlyList<TimedValue> rest, MetricSeries? live)
    {
        if (live is null) return rest;
        var now = live.Points.ToArray();
        if (rest.Count == 0) return now;
        var last = rest[^1].T;
        return rest.Concat(now.Where(p => p.T > last)).ToList();
    }

    /// <summary>The used share of one mount, in percent.</summary>
    public IReadOnlyList<TimedValue> MountPoints(string mount)
        => Disks?.Mounts.FirstOrDefault(m => m.Mount == mount)?.Points.Select(p => new TimedValue(p.Ts, p.Ratio * 100)).ToList()
           ?? new List<TimedValue>();

    /// <summary>The inode use of a mount at its newest point.</summary>
    public double? InodeRatio(string mount) => Disks?.Mounts.FirstOrDefault(m => m.Mount == mount)?.Points.LastOrDefault()?.InodeRatio;

    public static double Convert(string metric, double v) => metric switch
    {
        "mem" or "disk" => v * 100,
        "net_rx" or "net_tx" or "disk_read" or "disk_write" => Math.Max(v, 0) / 1_048_576,
        _ => v,
    };

    /// <summary>The health score at each time of the health metrics. It is the worst check. A muted check does not count.</summary>
    public IReadOnlyList<TimedValue> ScorePoints(MetricThresholds t, MutedSignals muted, bool linux)
    {
        var rows = new Dictionary<DateTime, Dictionary<string, double>>();
        lock (_gate)
        {
            foreach (var name in new[] { "cpu", "mem", "disk", "procs", "zombies", "pids", "files", "inodes", "mem_pressure", "psi_mem", "psi_mem_full", "psi_io_full" })
                foreach (var p in _series.GetValueOrDefault(name) ?? Array.Empty<TimedValue>())
                {
                    if (!rows.TryGetValue(p.T, out var row)) rows[p.T] = row = new();
                    row[name] = p.Value;
                }
        }
        var limits = t.HealthOrDefault;
        var result = new List<TimedValue>();
        foreach (var time in rows.Keys.Order())
        {
            var r = rows[time];
            var scores = new List<double>();
            void Add(string name, double scale, ThresholdPair pair, bool isMuted)
            {
                if (!isMuted && r.TryGetValue(name, out var v)) scores.Add(HealthScore.ScoreOf(v / scale, pair.Warn, pair.Critical));
            }
            Add("cpu", 100, t.Get(ThresholdMetric.Cpu), muted.Mutes(AlertMetric.Cpu));
            Add("mem", 100, t.Get(ThresholdMetric.Mem), muted.Mutes(AlertMetric.Mem));
            Add("disk", 100, t.Get(ThresholdMetric.Disk), muted.Mutes(AlertMetric.Disk));
            Add("procs", 1, t.Get(ThresholdMetric.Procs), muted.Mutes(HealthSignal.Procs));
            Add("zombies", 1, t.Get(ThresholdMetric.Zombies), muted.Mutes(HealthSignal.Zombies));
            Add("pids", 1, limits.Pids, muted.Mutes(HealthSignal.Pids));
            Add("files", 1, limits.Files, muted.Mutes(HealthSignal.Files));
            Add("inodes", 1, limits.Inodes, muted.Mutes(HealthSignal.Inodes));
            if (linux)
            {
                if (r.TryGetValue("psi_mem", out var some) && !muted.Mutes(HealthSignal.MemPressure))
                    scores.Add(HealthScore.PsiScore(some, r.GetValueOrDefault("psi_mem_full"), limits.MemPressure));
                Add("psi_io_full", 1, limits.IoPressure, muted.Mutes(HealthSignal.IoPressure));
            }
            else Add("mem_pressure", 1, limits.MacMemPressure, muted.Mutes(HealthSignal.MemPressure));
            if (scores.Count > 0) result.Add(new TimedValue(time, scores.Min()));
        }
        return result;
    }
}
