using Towertail.WinUI.Backend;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;
using Windows.UI;

namespace Towertail.WinUI.FullView;

/// <summary>
/// The chart series of one host for the window of the cursor. A short, live window reads the
/// memory series. A longer or zoomed window reads <see cref="HistoryStore.LoadRange"/>. A cloud host
/// reads the REST series of the cloud, then the live points after the last one.
/// </summary>
public sealed class WorkbenchData
{
    private static readonly TimeSpan HistoryEvery = TimeSpan.FromSeconds(15);

    private readonly ServerViewModel _vm;
    private readonly HistoryStore _history;
    private readonly WorkbenchCursor _cursor;
    private IReadOnlyList<HistoryPoint>? _rows;
    private (HostRange, (DateTime, DateTime)?) _rowsKey;
    private DateTime _rowsAt;
    private bool _loading;
    private (HostRange, (DateTime, DateTime)?, HostTab)? _cloudKey;
    private ((DateTime From, DateTime To) Window, TimeSpan Step)? _cloudView;
    private DateTime _cloudAt;

    /// <summary>Raised on the loading thread after new history rows arrive.</summary>
    public event Action? HistoryLoaded;

    /// <summary>The cloud series of a cloud host, else null.</summary>
    public WorkbenchSeries? Cloud { get; }

    public WorkbenchData(ServerViewModel vm, HistoryStore history, WorkbenchCursor cursor, WorkbenchSeries? cloud = null)
    {
        _vm = vm;
        _history = history;
        _cursor = cursor;
        Cloud = cloud;
        if (cloud is not null) cloud.Changed += () => HistoryLoaded?.Invoke();
    }

    public (DateTime From, DateTime To) Window => _cursor.Window(DateTime.UtcNow);

    private bool UsesHistory => Cloud is null && (_cursor.Zoom is not null || _cursor.Range != HostRange.H1);

    private bool IsLinux => string.Equals(_vm.HostOs, "linux", StringComparison.OrdinalIgnoreCase);

    /// <summary>Starts a history read when the window needs one and the rows are old. <paramref name="tab"/> picks the cloud metrics.</summary>
    public async void EnsureHistory(HostTab tab = HostTab.Overview)
    {
        if (Cloud is not null)
        {
            await EnsureCloudAsync(tab).ConfigureAwait(false);
            return;
        }
        if (!UsesHistory || _loading) return;
        var key = (_cursor.Range, _cursor.Zoom is { } z ? ((DateTime, DateTime)?)(z.From, z.To) : null);
        if (_rows is not null && key == _rowsKey && DateTime.UtcNow - _rowsAt < HistoryEvery) return;
        _loading = true;
        try
        {
            var w = Window;
            var rows = await Task.Run(() => _history.LoadRange(_vm.Node.Id, w.From, w.To, 300));
            _rows = rows;
            _rowsKey = key;
            _rowsAt = DateTime.UtcNow;
            HistoryLoaded?.Invoke();
        }
        catch { }
        finally { _loading = false; }
    }

    /// <summary>The window of a cloud load stays the same for 30 s, so a tab switch reuses the entry.</summary>
    private async Task EnsureCloudAsync(HostTab tab)
    {
        var key = (_cursor.Range, _cursor.Zoom is { } z ? ((DateTime, DateTime)?)(z.From, z.To) : null, tab);
        var now = DateTime.UtcNow;
        if (_cloudView is null || key != _cloudKey || now - _cloudAt >= HistoryEvery * 2)
        {
            _cloudKey = key;
            _cloudAt = now;
            _cloudView = (_cursor.Window(now), _cursor.HistoryStep(now));
        }
        var (window, step) = _cloudView.Value;
        await Cloud!.LoadAsync(_vm.Node.Id, tab, window, step, IsLinux).ConfigureAwait(false);
    }

    private IReadOnlyList<TimedValue> Memory(MetricSeries s) => SeriesMath.In(s.Points.ToArray(), Window);

    private IReadOnlyList<TimedValue> Pick(MetricSeries memory, Func<HistoryPoint, double?>? field, string metric)
    {
        if (Cloud is not null) return SeriesMath.In(Cloud.Points(metric, memory), Window);
        if (!UsesHistory || _rows is null || field is null) return Memory(memory);
        return _rows.Where(r => field(r) is not null).Select(r => new TimedValue(r.T, field(r)!.Value)).ToList();
    }

    public IReadOnlyList<ChartSeries> Lines(RailMetric m, bool dark)
    {
        Color C(int line = 0) => WB.Metric(m, dark, line);
        return m switch
        {
            RailMetric.Cpu => new[] { new ChartSeries("CPU", C(), Pick(_vm.CpuSeries, r => r.Cpu, "cpu")) },
            RailMetric.Mem => new[] { new ChartSeries("Memory", C(), Pick(_vm.MemSeries, r => r.Mem, "mem")) },
            RailMetric.Load => new[] { new ChartSeries("Load", C(), Pick(_vm.LoadSeries, r => r.Load, "load")) },
            RailMetric.Disk => new[] { new ChartSeries("Disk", C(), Pick(_vm.DiskSeries, r => r.Disk, "disk")) },
            RailMetric.Net => new[]
            {
                new ChartSeries("in", C(0), Pick(_vm.RxSeries, r => r.RxMBps, "net_rx")),
                new ChartSeries("out", C(1), Pick(_vm.TxSeries, r => r.TxMBps, "net_tx")),
            },
            RailMetric.Io => new[]
            {
                new ChartSeries("read", C(0), Pick(_vm.DiskReadSeries, null, "disk_read")),
                new ChartSeries("write", C(1), Pick(_vm.DiskWriteSeries, null, "disk_write")),
            },
            _ => new[] { new ChartSeries("Health", C(), HealthLine()) },
        };
    }

    private IReadOnlyList<TimedValue> HealthLine()
    {
        if (Cloud is null) return Pick(_vm.HealthScoreSeries, r => r.Score, "health");
        var rest = Cloud.ScorePoints(_vm.Thresholds, _vm.Muted, IsLinux);
        return SeriesMath.In(WorkbenchSeries.Append(rest, _vm.HealthScoreSeries), Window);
    }

    public static string Rate(double mbps) => Bytes(mbps * 1_048_576) + "/s";

    public static string Bytes(double bytes) => StringFormatters.Bytes((long)Math.Max(0, bytes));

    public ChartFormat Format(RailMetric m)
    {
        var t = _vm.Thresholds;
        ChartFormat Pct(ThresholdMetric tm)
        {
            var pair = t.Get(tm);
            return new ChartFormat(v => $"{v:0}%", 100, pair.Warn * 100, pair.Critical * 100);
        }
        return m switch
        {
            RailMetric.Cpu => Pct(ThresholdMetric.Cpu),
            RailMetric.Mem => Pct(ThresholdMetric.Mem),
            RailMetric.Disk => Pct(ThresholdMetric.Disk),
            RailMetric.Load => new ChartFormat(v => v.ToString("0.00")),
            RailMetric.Health => new ChartFormat(v => v.ToString("0"), 100, 80, 40),
            _ => new ChartFormat(Rate),
        };
    }

    /// <summary>The value of a metric line as text, at time <paramref name="t"/>.</summary>
    public string ValueText(RailMetric m, IReadOnlyList<ChartSeries> lines, DateTime t, int line = 0)
    {
        var v = line < lines.Count ? SeriesMath.ValueAt(lines[line].Points, t) : null;
        if (v is not double x) return "—";
        return m switch
        {
            RailMetric.Cpu or RailMetric.Mem or RailMetric.Disk => $"{x:0}%",
            RailMetric.Load => x.ToString("0.00"),
            RailMetric.Health => x.ToString("0"),
            _ => Bytes(x * 1_048_576),
        };
    }

    public Color? Tint(RailMetric m, double? v, bool dark)
    {
        if (v is not double x) return null;
        var t = _vm.Thresholds;
        HealthLevel Level(ThresholdMetric tm)
        {
            var p = t.Get(tm);
            return x >= p.Critical * 100 ? HealthLevel.Critical : x >= p.Warn * 100 ? HealthLevel.Warn : HealthLevel.Nominal;
        }
        var level = m switch
        {
            RailMetric.Cpu => Level(ThresholdMetric.Cpu),
            RailMetric.Mem => Level(ThresholdMetric.Mem),
            RailMetric.Disk => Level(ThresholdMetric.Disk),
            RailMetric.Health => HealthScore.LevelOf(x),
            _ => HealthLevel.Nominal,
        };
        return level == HealthLevel.Nominal ? null : WB.Level(level, dark);
    }

    public IEnumerable<RailTile> Tiles(DateTime? at, bool dark)
    {
        var now = DateTime.UtcNow;
        var w = Window;
        foreach (var m in Enum.GetValues<RailMetric>())
        {
            var lines = Lines(m, dark);
            var t = at ?? now;
            var first = lines.Count > 0 ? SeriesMath.ValueAt(lines[0].Points, t) : null;
            var second = lines.Count > 1 ? SeriesMath.ValueAt(lines[1].Points, t) : null;
            string sub = m switch
            {
                RailMetric.Cpu => _vm.Cores is int c ? $"of {c} cores" : "",
                RailMetric.Mem => _vm.MemTotal is long mt ? $"of {mt / 1_073_741_824.0:0.0} GB" : "",
                RailMetric.Load => "1 min average",
                RailMetric.Disk => "worst mount",
                RailMetric.Net => "in · out",
                RailMetric.Io => "read · write",
                _ => _vm.HealthScore.Worst is { } worst ? "worst: " + worst.Label.ToLowerInvariant() : "no data",
            };
            yield return new RailTile(
                m, ValueText(m, lines, t), m.HasTwoSeries() ? lines[0].Color : Tint(m, first, dark),
                m.HasTwoSeries() ? ValueText(m, lines, t, 1) : null, m.HasTwoSeries() ? lines[1].Color : null,
                sub, lines, m is RailMetric.Cpu or RailMetric.Mem or RailMetric.Disk or RailMetric.Health ? 100 : null,
                at, w);
        }
    }
}
