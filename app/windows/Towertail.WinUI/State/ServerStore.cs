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

    private readonly NodeStore _nodes;
    private readonly ServerSettings _settings;
    private readonly HistoryStore _history;
    private ThresholdNotifier? _notifier;
    private readonly object _hydrationLock = new();
    private readonly HashSet<Guid> _hydratedProcs = new();

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
                if (Servers[i].Node.Id == id) { Servers.RemoveAt(i); return; }
        };
        nodes.NodeUpdated += (_, id) =>
        {
            var n = nodes.ById(id); if (n == null) return;
            var vm = Find(id); if (vm != null) vm.UpdateNode(n);
        };
    }

    public void AttachNotifier(ThresholdNotifier notifier) => _notifier = notifier;

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

        vm.Ingest(sample);

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
            TxMBps: vm.TxMBps));

        if (sample.Disks is { Count: > 0 } ds)
            _history.AppendDiskCapacity(nodeId, sample.Ts, ds);

        if (sample.DiskIo is { } dio)
            _history.AppendDiskIO(nodeId, sample.Ts, dio.ReadBps, dio.WriteBps, dio.Devices);

        if (sample.Procs is { } p)
            _history.AppendProcs(nodeId, sample.Ts, p.Root, p.Items);

        _notifier?.Evaluate(vm);
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
        foreach (var p in points)
        {
            if (p.Cpu is double c) vm.CpuSeries.Append(p.T, c);
            if (p.Mem is double m) vm.MemSeries.Append(p.T, m);
            if (p.Disk is double d) vm.DiskSeries.Append(p.T, d);
            if (p.Net is double nt) vm.NetSeries.Append(p.T, nt);
            if (p.RxMBps is double rx) vm.RxSeries.Append(p.T, rx);
            if (p.TxMBps is double tx) vm.TxSeries.Append(p.T, tx);
        }
        return vm;
    }
}
