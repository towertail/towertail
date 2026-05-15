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

    public MetricSeries CpuSeries { get; } = new();
    public MetricSeries MemSeries { get; } = new();
    public MetricSeries DiskSeries { get; } = new();
    public MetricSeries NetSeries { get; } = new();
    public MetricSeries RxSeries { get; } = new();
    public MetricSeries TxSeries { get; } = new();
    public DiskSeries Disk { get; } = new();
    public ProcSeries Procs { get; } = new();

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
        _lastSampleTs = now;

        if (s.DiskIo is { } dio)
            Disk.AppendIo(now, dio.Devices);

        if (s.Procs is { } p)
            Procs.Append(now, p.Root, p.Items);

        if (s.Ports is { } pl)
        {
            Ports = pl;
            PortsAvailable = true;
        }
    }
}
