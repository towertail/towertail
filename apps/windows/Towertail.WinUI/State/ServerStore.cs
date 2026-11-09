using System.Collections.ObjectModel;
using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.State;

/// <summary>
/// Fan-out hub: samples land here from the collector, land in the right per-host
/// <see cref="ServerViewModel"/>, and are persisted into <see cref="HistoryStore"/>.
/// Mirrors ServerStore.swift.
/// </summary>
public sealed partial class ServerStore : ObservableObject
{
    public ObservableCollection<ServerViewModel> Servers { get; } = new();

    /// <summary>Tray icon state. Mirrors aggregateState in ServerStore.swift.</summary>
    [ObservableProperty] private HealthLevel _aggregateLevel;

    private readonly NodeStore _nodes;
    private readonly ServerSettings _settings;
    private readonly HistoryStore _history;
    private ThresholdNotifier? _notifier;
    private Logger? _logger;
    private readonly object _hydrationLock = new();
    private readonly HashSet<Guid> _hydratedProcs = new();
    private CancellationTokenSource? _watchdog;
    private DateTime? _lastStaleCheck;

    /// <summary>Watchdog period.</summary>
    public static readonly TimeSpan StaleCheckEvery = TimeSpan.FromSeconds(5);

    public ServerStore(NodeStore nodes, ServerSettings settings, HistoryStore history)
    {
        _nodes = nodes;
        _settings = settings;
        _history = history;
        foreach (var n in nodes.Nodes) Servers.Add(CreateVm(n));

        nodes.NodeAdded += (_, id) =>
        {
            var n = nodes.ById(id); if (n == null) return;
            Servers.Add(CreateVm(n));
        };
        nodes.NodeRemoved += (_, id) =>
        {
            for (int i = 0; i < Servers.Count; i++)
                if (Servers[i].Node.Id == id) { Servers.RemoveAt(i); break; }
            UpdateAggregateLevel();
        };
        nodes.NodeUpdated += (_, id) =>
        {
            var n = nodes.ById(id); if (n == null) return;
            var vm = Find(id); if (vm != null) vm.UpdateNode(n);
            UpdateAggregateLevel();
        };
    }

    public void AttachNotifier(ThresholdNotifier notifier) => _notifier = notifier;
    public void AttachLogger(Logger logger) => _logger = logger;

    public ServerViewModel? Find(Guid id)
    {
        foreach (var vm in Servers)
            if (vm.Node.Id == id) return vm;
        return null;
    }

    public void Ingest(Guid nodeId, Sample sample)
    {
        var vm = Find(nodeId);
        if (vm == null) return;

        vm.Thresholds = MetricThresholds.Effective(_settings.Thresholds, vm.Node.CustomThresholds);
        vm.AlertRules = vm.Node.EffectiveAlerts(_settings.Alerts);
        var priorHealth = vm.Health.Level;
        vm.Ingest(sample);
        if (vm.Health.Level != priorHealth)
        {
            var msg = $"thresholds host={vm.Node.DisplayName} health: {priorHealth} -> {vm.Health.Level}";
            if (vm.Health.Reasons.Count > 0) msg += $" reasons=\"{vm.Health.Body}\"";
            if (vm.Health.Level == HealthLevel.Nominal) _logger?.Info(msg);
            else _logger?.Warning(msg);
        }

        var frac = sample.Disks is { Count: > 0 } disks
            ? disks.Max(d => d.Total > 0 ? (double)d.Used / d.Total : 0)
            : (double?)null;
        var net = (vm.RxMBps ?? 0) + (vm.TxMBps ?? 0);
        _history.Append(nodeId, new HistoryPoint(
            T: sample.Ts,
            Cpu: vm.CpuPct,
            Mem: vm.MemPct,
            Disk: frac is double f ? f * 100.0 : null,
            Net: net,
            RxMBps: vm.RxMBps,
            TxMBps: vm.TxMBps,
            Procs: sample.Health?.Procs));

        if (sample.Disks is { Count: > 0 } ds)
            _history.AppendDiskCapacity(nodeId, sample.Ts, ds);

        if (sample.DiskIo is { } dio)
            _history.AppendDiskIO(nodeId, sample.Ts, dio.ReadBps, dio.WriteBps, dio.Devices);

        if (sample.Procs is { Skipped: not true } p)
            _history.AppendProcs(nodeId, sample.Ts, p.Root, p.Items);

        _notifier?.Evaluate(vm);
        UpdateAggregateLevel();
    }

    /// <summary>A failed poll breaks the sample stream, so sustain must start again.</summary>
    public void MarkUnreachable(Guid nodeId, string? reason = null)
    {
        var vm = Find(nodeId);
        if (vm == null) return;
        if (reason is { Length: > 0 }) vm.MarkOffline(reason);
        else vm.ResetAlerts();
        UpdateAggregateLevel();
    }

    /// <summary>No-data limit: three missed polls, but not less than 30 s.</summary>
    public static TimeSpan StaleLimit(int pollSeconds) => TimeSpan.FromSeconds(Math.Max(3 * pollSeconds, 30));

    /// <summary>
    /// Watchdog step for all enabled hosts. A tick that comes much later than
    /// the period means the PC slept, so each host starts its check again.
    /// </summary>
    public void CheckStale(DateTime now)
    {
        if (_lastStaleCheck is DateTime last && now - last > 3 * StaleCheckEvery)
            foreach (var vm in Servers) vm.ResetStaleBaseline(now);
        _lastStaleCheck = now;
        foreach (var vm in Servers)
        {
            if (!vm.Node.Enabled) continue;
            var limit = StaleLimit(_settings.PollingInterval(vm.Node.Kind));
            if (vm.CheckStale(now, limit))
                _logger?.Warning($"state host={vm.Node.DisplayName} -> offline reason=\"no data\" age_s={(int)(now - (vm.LastIngestAt ?? vm.StaleBaseline)).TotalSeconds}");
            _notifier?.EvaluateNoData(vm, now);
        }
        UpdateAggregateLevel();
    }

    /// <summary>
    /// Runs <see cref="CheckStale"/> every 5 s until <see cref="StopStaleWatchdog"/>.
    /// <paramref name="marshal"/> moves each check to the UI thread; null runs it in place.
    /// </summary>
    public void StartStaleWatchdog(Func<Action, Task>? marshal)
    {
        if (_watchdog != null) return;
        var cts = _watchdog = new CancellationTokenSource();
        _ = Task.Run(async () =>
        {
            using var timer = new PeriodicTimer(StaleCheckEvery);
            try
            {
                while (await timer.WaitForNextTickAsync(cts.Token).ConfigureAwait(false))
                {
                    if (marshal is null) CheckStale(DateTime.UtcNow);
                    else await marshal(() => CheckStale(DateTime.UtcNow)).ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException) { }
        });
    }

    public void StopStaleWatchdog()
    {
        _watchdog?.Cancel();
        _watchdog = null;
    }

    /// <summary>
    /// Worst sustained level over the hosts. A host counts only when its node
    /// opts in with IconOnWarn / IconOnCritical for that level. Snoozed hosts do not count.
    /// </summary>
    private void UpdateAggregateLevel()
    {
        var level = HealthLevel.Nominal;
        foreach (var vm in Servers)
        {
            if (vm.Node.IsSnoozed) continue;
            var host = Enum.GetValues<AlertMetric>().Max(vm.AlertLevel);
            if (host == HealthLevel.Critical && vm.Node.IconOnCritical) { level = HealthLevel.Critical; break; }
            if (host == HealthLevel.Warn && vm.Node.IconOnWarn) level = HealthLevel.Warn;
        }
        AggregateLevel = level;
    }

    public async Task EnsureProcsHydratedAsync(Guid nodeId)
    {
        lock (_hydrationLock)
            if (!_hydratedProcs.Add(nodeId)) return;

        var vm = Find(nodeId);
        if (vm == null) return;
        var rows = await Task.Run(() => _history.LoadRecentProcs(nodeId)).ConfigureAwait(false);
        vm.Procs.Replace(rows.Select(r => new ProcSeries.Snapshot(r.T, r.Root, r.Items)));
        vm.Procs.Hydrated = true;
    }

    private ServerViewModel CreateVm(Node n)
    {
        var vm = new ServerViewModel(n);
        // Rehydrate metric series from disk so a restart doesn't look "cold".
        var points = _history.LoadRecent(n.Id);
        // A host with history counts as connected, so the watchdog checks it after a restart.
        vm.EverConnected = points.Count > 0;
        foreach (var p in points)
        {
            if (p.Cpu is double c) vm.CpuSeries.Append(p.T, c);
            if (p.Mem is double m) vm.MemSeries.Append(p.T, m);
            if (p.Disk is double d) vm.DiskSeries.Append(p.T, d);
            if (p.Net is double nt) vm.NetSeries.Append(p.T, nt);
            if (p.RxMBps is double rx) vm.RxSeries.Append(p.T, rx);
            if (p.TxMBps is double tx) vm.TxSeries.Append(p.T, tx);
            if (p.Procs is double pc) vm.ProcCountSeries.Append(p.T, pc);
        }
        return vm;
    }
}
