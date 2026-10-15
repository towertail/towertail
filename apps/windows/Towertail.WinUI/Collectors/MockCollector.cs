using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// No-op collector for previews / tests. Emits a deterministic sine wave of CPU values so
/// XAML previews show a lively card without real processes.
/// </summary>
public sealed class MockCollector : ICollector
{
    private readonly ServerStore _store;
    private readonly NodeStore _nodes;
    private CancellationTokenSource? _cts;
    private readonly Random _rng = new(42);

    public MockCollector(NodeStore nodes, ServerStore store)
    {
        _nodes = nodes;
        _store = store;
    }

    public void Start()
    {
        _cts = new CancellationTokenSource();
        _ = Task.Run(() => LoopAsync(_cts.Token));
    }

    public Task StopAsync()
    {
        _cts?.Cancel();
        return Task.CompletedTask;
    }

    public Task RefreshAsync(Guid nodeId) => Task.CompletedTask;

    private async Task LoopAsync(CancellationToken ct)
    {
        int tick = 0;
        while (!ct.IsCancellationRequested)
        {
            foreach (var node in _nodes.Nodes.ToList())
            {
                var phase = (tick + node.Id.GetHashCode()) * 0.1;
                var cpu = 30 + 20 * Math.Sin(phase) + _rng.NextDouble() * 5;
                var sample = new Sample(
                    V: 1,
                    Ts: DateTime.UtcNow,
                    Host: new HostInfo(node.DisplayName, "windows", "amd64", "10.0", 0, "mock", null),
                    Cpu: new CpuInfo(cpu, 0, 0, 0, 8),
                    Mem: new MemInfo((long)(8_000_000_000 * 0.5), 16_000_000_000),
                    Swap: new MemInfo(0, 0),
                    Disks: new[] { new DiskSample("C:", "NTFS", 200_000_000_000, 500_000_000_000) },
                    DiskIo: null,
                    Net: new NetInfo(0, 0, 0, 0),
                    Procs: null,
                    Ports: null,
                    Errors: Array.Empty<string>());
                _store.Ingest(node.Id, sample);
            }
            tick++;
            try { await Task.Delay(TimeSpan.FromSeconds(2), ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
    }
}
