using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
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

        _servers.Ingest(n.Id, SampleFixture.Make(diskPct: 97));
        _servers.AggregateLevel.Should().Be(HealthLevel.Critical);

        _servers.Ingest(n.Id, SampleFixture.Make(diskPct: 90));
        _servers.AggregateLevel.Should().Be(HealthLevel.Warn);

        _servers.Ingest(n.Id, SampleFixture.Make(diskPct: 40));
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void AggregateLevelSkipsHostsThatOptOut()
    {
        var n = _nodes.Nodes[0] with { IconOnWarn = false, IconOnCritical = false };
        _nodes.Update(n);

        _servers.Ingest(n.Id, SampleFixture.Make(diskPct: 97));
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);

        _nodes.Update(n with { IconOnCritical = true });
        _servers.AggregateLevel.Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void AggregateLevelSkipsSnoozedHosts()
    {
        var n = _nodes.Nodes[0];
        _nodes.SetSnooze(n.Id, DateTime.UtcNow.AddHours(1));

        _servers.Ingest(n.Id, SampleFixture.Make(diskPct: 97));
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);

        _nodes.SetSnooze(n.Id, null);
        _servers.AggregateLevel.Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void EffectiveThresholdsKeepOrgCpuWhenHostSetsMemOnly()
    {
        var settings = new ServerSettings(_settings)
        {
            Thresholds = MetricThresholds.Defaults with { CpuWarn = 0.3, CpuCritical = 0.4 },
        };
        var servers = new ServerStore(_nodes, settings, _store);
        var n = _nodes.Nodes[0] with { ThresholdOverrides = new ThresholdOverrides(Mem: new ThresholdPair(0.2, 0.3), MemPressure: new ThresholdPair(50, 60)) };
        _nodes.Update(n);

        servers.Ingest(n.Id, SampleFixture.Make(diskPct: 10));
        var vm = servers.Find(n.Id)!;
        (vm.Thresholds.CpuWarn, vm.Thresholds.CpuCritical).Should().Be((0.3, 0.4));
        (vm.Thresholds.MemWarn, vm.Thresholds.MemCritical).Should().Be((0.2, 0.3));
        vm.MemPressureOverridden.Should().BeTrue();
    }

    private static LiveSummary Summary(double cpu) => new(DateTime.UtcNow, cpu, 0.1, 0.1, 0, 0, 0);

    [Fact]
    public void SummaryForLocalNodeIsIgnored()
    {
        var n = _nodes.Nodes[0];
        _servers.IngestSummary(n.Id, Summary(80));
        _servers.Find(n.Id)!.CpuPct.Should().BeNull();

        var cloud = Node.CloudOnly(Guid.NewGuid(), "cloud");
        _nodes.Add(cloud);
        _servers.IngestSummary(cloud.Id, Summary(80));
        _servers.Find(cloud.Id)!.CpuPct.Should().BeApproximately(80, 0.0001);
    }

    [Fact]
    public void DetailForLocalNodeIsIgnored()
    {
        var n = _nodes.Nodes[0];
        _servers.ApplyDetail(n.Id, DetailTab.Health, new LiveDetail(DateTime.UtcNow, Health: new HealthInfo(30_000)));
        _servers.Find(n.Id)!.Health.Level.Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void ReachabilityEventMarksOffline()
    {
        var cloud = Node.CloudOnly(Guid.NewGuid(), "cloud");
        _nodes.Add(cloud);
        _servers.IngestSummary(cloud.Id, Summary(10));
        var vm = _servers.Find(cloud.Id)!;

        _servers.ApplyReachability(new StreamEvent(cloud.Id, "reachability", "critical", "reachability_changed"));
        vm.OfflineReason.Should().NotBeNull();

        _servers.ApplyReachability(new StreamEvent(cloud.Id, "reachability", "ok", "reachability_changed"));
        vm.OfflineReason.Should().BeNull();
    }

    [Fact]
    public void ReachabilityEventOfOtherKindOrLocalNodeIsIgnored()
    {
        var local = _nodes.Nodes[0];
        _servers.ApplyReachability(new StreamEvent(local.Id, "reachability", "critical", "reachability_changed"));
        _servers.Find(local.Id)!.OfflineReason.Should().BeNull();

        var cloud = Node.CloudOnly(Guid.NewGuid(), "cloud");
        _nodes.Add(cloud);
        _servers.ApplyReachability(new StreamEvent(cloud.Id, "cpu", "critical", "alert_opened"));
        _servers.Find(cloud.Id)!.OfflineReason.Should().BeNull();
    }

    [Fact]
    public void CloudNodeSampleDoesNotNotify()
    {
        var settings = new ServerSettings(_settings) { NotificationsEnabled = true, NotifyCritical = true, NotifyDebounceSeconds = 0 };
        var notifier = new ThresholdNotifier(settings, _nodes);
        var raised = 0;
        notifier.Raised += (_, _) => raised++;
        _servers.AttachNotifier(notifier);

        var cloud = Node.CloudOnly(Guid.NewGuid(), "cloud");
        _nodes.Add(cloud);
        _servers.Ingest(cloud.Id, SampleFixture.Make(diskPct: 99));
        raised.Should().Be(0);

        var local = _nodes.Nodes[0];
        _servers.Ingest(local.Id, SampleFixture.Make(diskPct: 99));
        raised.Should().BeGreaterThan(0);
    }
}
