using CommunityToolkit.Mvvm.ComponentModel;

namespace Towertail.WinUI.State;

/// <summary>
/// View model for a single card in the popover — wraps a <see cref="Node"/> + derived metrics
/// computed from cumulative sample counters. Mirrors ServerViewModel.swift.
/// </summary>
public sealed partial class ServerViewModel : ObservableObject
{
    public Node Node { get; private set; }

    [ObservableProperty] private double? _cpuPct;
    [ObservableProperty] private double? _memPct;
    [ObservableProperty] private double? _diskMaxPct;
    [ObservableProperty] private double? _rxMBps;
    [ObservableProperty] private double? _txMBps;
    [ObservableProperty] private DateTime? _lastSeen;
    [ObservableProperty] private string? _samplerVersion;
    [ObservableProperty] private string? _offlineReason;
    [ObservableProperty] private int _errorCount;
    [ObservableProperty] private HealthStatus _health = HealthStatus.Nominal;
    /// <summary>Latest <c>procs.total</c> when the sampler skipped the per-process scan.</summary>
    [ObservableProperty] private int? _procsSkippedTotal;

    /// <summary>Effective thresholds for this host. Set by <see cref="ServerStore"/> before each ingest.</summary>
    public MetricThresholds Thresholds { get; set; } = MetricThresholds.Defaults;

    /// <summary>Effective alert rules for this host. Set by <see cref="ServerStore"/> before each ingest.</summary>
    public AlertRules AlertRules { get; set; } = AlertRules.Defaults;

    /// <summary>
    /// True when the host shows real memory stress (PSI, macOS pressure, or fast swap growth);
    /// false when it shows none; null when the host gives no signal.
    /// </summary>
    public bool? MemPressured { get; private set; }

    public const double PsiMemFullPressured = 5, PsiMemSomePressured = 20;
    public const double SwapGrowthPressuredBps = 1_048_576;

    public MetricSeries CpuSeries { get; } = new();
    public MetricSeries MemSeries { get; } = new();
    public MetricSeries DiskSeries { get; } = new();
    public MetricSeries NetSeries { get; } = new();
    public MetricSeries RxSeries { get; } = new();
    public MetricSeries TxSeries { get; } = new();
    public DiskSeries Disk { get; } = new();
    public ProcSeries Procs { get; } = new();
    /// <summary>Raw process count per sample (not a percentage).</summary>
    public MetricSeries ProcCountSeries { get; } = new();

    /// <summary>
    /// Latest-known per-process port snapshot. Snapshot-only (not
    /// time-windowed) — the sampler refreshes every 10s by default and
    /// re-emits the cached snapshot in between, so there's no useful
    /// per-tick history. <c>PortsAvailable</c> is set the first time a
    /// sample carries a <c>ports</c> payload so the UI can show "ports
    /// disabled" separately from "ports loading".
    /// </summary>
    [ObservableProperty] private PortList? _ports;
    [ObservableProperty] private bool _portsAvailable;

    private long? _lastRxCum;
    private long? _lastTxCum;
    private DateTime? _lastSampleTs;
    private long? _lastSwapUsed;
    private readonly Dictionary<AlertMetric, SustainWindow> _windows = new()
    {
        [AlertMetric.Cpu] = new(), [AlertMetric.Mem] = new(), [AlertMetric.Disk] = new(), [AlertMetric.Health] = new(),
    };

    public ServerViewModel(Node node) { Node = node; }

    public void UpdateNode(Node node) => Node = node;

    public void Ingest(Sample s)
    {
        var now = s.Ts;
        LastSeen = now;
        SamplerVersion = s.Host.Sampler;
        ErrorCount = s.Errors.Count;

        CpuPct = Math.Clamp(s.Cpu.Pct, 0, 100);
        CpuSeries.Append(now, CpuPct.Value);

        var memFrac = s.Mem.Total > 0 ? (double)s.Mem.Used / s.Mem.Total : 0;
        MemPct = memFrac * 100.0;
        MemSeries.Append(now, MemPct.Value);

        if (s.Disks is { Count: > 0 } disks)
        {
            double max = 0;
            foreach (var d in disks)
                if (d.Total > 0)
                    max = Math.Max(max, (double)d.Used / d.Total);
            DiskMaxPct = max * 100.0;
            DiskSeries.Append(now, DiskMaxPct.Value);
            Disk.AppendCapacity(now, disks);
        }

        if (s.Net is { } net)
        {
            if (_lastRxCum is long prevRx && _lastTxCum is long prevTx && _lastSampleTs is DateTime prevT)
            {
                var elapsed = (now - prevT).TotalSeconds;
                if (elapsed > 0)
                {
                    var rx = Math.Max(0, net.RxCum - prevRx) / elapsed;
                    var tx = Math.Max(0, net.TxCum - prevTx) / elapsed;
                    RxMBps = rx / 1_048_576.0;
                    TxMBps = tx / 1_048_576.0;
                    RxSeries.Append(now, RxMBps.Value);
                    TxSeries.Append(now, TxMBps.Value);
                    NetSeries.Append(now, RxMBps.Value + TxMBps.Value);
                }
            }
            _lastRxCum = net.RxCum;
            _lastTxCum = net.TxCum;
        }
        MemPressured = MemPressureOf(s, _lastSwapUsed, _lastSampleTs is DateTime prevTs ? (now - prevTs).TotalSeconds : null);
        _lastSwapUsed = s.Swap.Total > 0 ? s.Swap.Used : null;
        _lastSampleTs = now;

        if (s.DiskIo is { } dio)
            Disk.AppendIo(now, dio.Devices);

        if (s.Procs is { } p)
        {
            ProcsSkippedTotal = p.Skipped == true ? p.Total : null;
            if (p.Skipped != true) Procs.Append(now, p.Root, p.Items);
        }

        if (s.Health is { } h)
            ProcCountSeries.Append(now, h.Procs);
        Health = HealthStatus.Evaluate(s.Health, s.Disks, Thresholds);
        foreach (var (m, w) in _windows)
            w.Record(now, m == AlertMetric.Health ? Health.LevelExcluding(HealthSignal.Inodes) : RawLevel(m),
                     AlertRules.For(m).SustainSeconds);

        if (s.Ports is { } pl)
        {
            Ports = pl;
            PortsAvailable = true;
        }
    }

    /// <summary>Unsustained level of the latest sample. Drives the card colors.</summary>
    public HealthLevel RawLevel(AlertMetric m) => m switch
    {
        AlertMetric.Cpu => Level(CpuPct, Thresholds.CpuWarn, Thresholds.CpuCritical),
        AlertMetric.Mem => Level(MemPct, Thresholds.MemWarn, Thresholds.MemCritical),
        AlertMetric.Disk => Level(DiskMaxPct, Thresholds.DiskWarn, Thresholds.DiskCritical),
        _ => Health.Level,
    };

    /// <summary>Sustain-gated level that drives notifications.</summary>
    public HealthLevel AlertLevel(AlertMetric m)
    {
        var rule = AlertRules.For(m);
        var sustained = _windows[m].Level(rule.SustainSeconds, AlertRules.Tolerance);
        return m switch
        {
            AlertMetric.Mem => MemPressured switch
            {
                true when RawLevel(m) >= HealthLevel.Warn => HealthLevel.Critical,
                false => sustained > HealthLevel.Warn ? HealthLevel.Warn : sustained,
                _ => sustained,
            },
            // Inodes run out like disk space: no sustain.
            AlertMetric.Health => (HealthLevel)Math.Max((int)sustained, (int)Health.TintOf(HealthSignal.Inodes)),
            _ => sustained,
        };
    }

    /// <summary>Drop sustain history, e.g. after the host was unreachable.</summary>
    public void ResetAlerts()
    {
        foreach (var w in _windows.Values) w.Reset();
        _lastSwapUsed = null;
    }

    private static HealthLevel Level(double? pct, double warn, double critical)
    {
        if (pct is not double v) return HealthLevel.Nominal;
        var f = v / 100.0;
        return f >= critical ? HealthLevel.Critical : f >= warn ? HealthLevel.Warn : HealthLevel.Nominal;
    }

    private static bool? MemPressureOf(Sample s, long? prevSwapUsed, double? elapsed)
    {
        if (s.Health?.Psi is { } psi) return psi.MemFull >= PsiMemFullPressured || psi.MemSome >= PsiMemSomePressured;
        if (s.Health?.MemPressure is int mp) return mp == 4;
        if (s.Swap.Total > 0 && prevSwapUsed is long prev && elapsed is double dt && dt > 0)
            return (s.Swap.Used - prev) / dt >= SwapGrowthPressuredBps;
        return null;
    }
}
