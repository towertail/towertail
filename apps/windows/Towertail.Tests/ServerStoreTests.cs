using FluentAssertions;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class ServerStoreTests : IAsyncLifetime
{
    private string _settings = "";
    private string _history = "";
    private HistoryStore _store = null!;
    private NodeStore _nodes = null!;
    private ServerStore _servers = null!;

    public ValueTask InitializeAsync()
    {
        _settings = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _history = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        _store = new HistoryStore(_history);
        _nodes = new NodeStore(_settings);
        _servers = new ServerStore(_nodes, new ServerSettings(_settings), _store);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _store.DisposeAsync();
        if (File.Exists(_settings)) File.Delete(_settings);
        if (File.Exists(_history)) File.Delete(_history);
    }

    // Disk has no sustain by default, so one sample sets the alert level.
    [Fact]
    public void AggregateLevelFollowsWorstHost()
    {
        var n = _nodes.Nodes[0];
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);

        _servers.Ingest(n.Id, Make(n, diskPct: 97));
        _servers.AggregateLevel.Should().Be(HealthLevel.Critical);

        _servers.Ingest(n.Id, Make(n, diskPct: 90));
        _servers.AggregateLevel.Should().Be(HealthLevel.Warn);

        _servers.Ingest(n.Id, Make(n, diskPct: 40));
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void AggregateLevelSkipsHostsThatOptOut()
    {
        var n = _nodes.Nodes[0] with { IconOnWarn = false, IconOnCritical = false };
        _nodes.Update(n);

        _servers.Ingest(n.Id, Make(n, diskPct: 97));
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);

        _nodes.Update(n with { IconOnCritical = true });
        _servers.AggregateLevel.Should().Be(HealthLevel.Critical);
    }

    private static Sample Make(Node n, double diskPct)
    {
        var diskUsed = (long)(diskPct / 100.0 * 500_000_000_000L);
        return new Sample(
            1, DateTime.UtcNow,
            new HostInfo(n.DisplayName, "windows", "amd64", "10.0", 0, "s", null),
            new CpuInfo(10, 0, 0, 0, 8),
            new MemInfo(4_000_000_000, 16_000_000_000),
            new MemInfo(0, 0),
            new[] { new DiskSample("C:", "NTFS", diskUsed, 500_000_000_000) },
            null, null, null, null, Array.Empty<string>());
    }
}
