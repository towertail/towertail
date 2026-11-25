using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ServerViewModelTests
{
    [Fact]
    public void IngestUpdatesCpuMemDisk()
    {
        var n = Node.LocalWindows("pc");
        var vm = new ServerViewModel(n);
        var sample = new Sample(
            V: 1,
            Ts: DateTime.UtcNow,
            Host: new HostInfo("pc", "windows", "amd64", "10.0", 0, "s", null),
            Cpu: new CpuInfo(75, 0, 0, 0, 8),
            Mem: new MemInfo(8_000_000_000, 16_000_000_000),
            Swap: new MemInfo(0, 0),
            Disks: new[] { new DiskSample("C:", "NTFS", 250_000_000_000, 500_000_000_000) },
            DiskIo: null,
            Net: new NetInfo(0, 0, 0, 0),
            Procs: null,
            Ports: null,
            Errors: Array.Empty<string>());
        vm.Ingest(sample);

        vm.CpuPct.Should().BeApproximately(75, 0.0001);
        vm.MemPct.Should().BeApproximately(50, 0.0001);
        vm.DiskMaxPct.Should().BeApproximately(50, 0.0001);
    }

    [Fact]
    public void NetMbpsComesFromCumulativeDelta()
    {
        var n = Node.LocalWindows();
        var vm = new ServerViewModel(n);
        var t = DateTime.UtcNow;
        vm.Ingest(Make(t, rxCum: 1_048_576 * 0));
        vm.Ingest(Make(t.AddSeconds(1), rxCum: 1_048_576 * 5));
        vm.RxMBps.Should().NotBeNull();
        vm.RxMBps!.Value.Should().BeApproximately(5, 0.0001);
    }

    [Fact]
    public void IngestSummarySetsCardValues()
    {
        var vm = new ServerViewModel(Node.CloudOnly(Guid.NewGuid(), "cloud"));
        var t = DateTime.UtcNow;
        var facts = new HostFacts(Os: "linux", Sampler: "0.2.0+abc");
        vm.IngestSummary(new LiveSummary(t, 42.0, 0.5, 0.7, 2_097_152, 1_048_576, 1.5, 300, new HealthBrief(new HealthInfo(300)), facts), t);

        vm.CpuPct.Should().BeApproximately(42, 0.0001);
        vm.MemPct.Should().BeApproximately(50, 0.0001);
        vm.DiskMaxPct.Should().BeApproximately(70, 0.0001);
        vm.RxMBps.Should().BeApproximately(2, 0.0001);
        vm.TxMBps.Should().BeApproximately(1, 0.0001);
        vm.LastSeen.Should().Be(t);
        vm.HostOs.Should().Be("linux");
        vm.SamplerVersion.Should().Be("0.2.0+abc");
        vm.LastIngestAt.Should().Be(t);
        vm.EverConnected.Should().BeTrue();
        vm.LiveSource.Should().Be(LiveSource.Cloud);
        vm.CpuSeries.Points.Should().ContainSingle();
        vm.NetSeries.Points.Should().ContainSingle();
        vm.ProcCountSeries.Points.Should().ContainSingle();
    }

    [Fact]
    public void IngestSummaryClearsTheOfflineState()
    {
        var vm = new ServerViewModel(Node.CloudOnly(Guid.NewGuid(), "cloud"));
        vm.MarkOffline("Cloud lost the host");
        var t = DateTime.UtcNow;
        vm.IngestSummary(new LiveSummary(t, 1, 0.1, 0.1, 0, 0, 0), t);
        vm.OfflineReason.Should().BeNull();
        vm.LastError.Should().BeNull();
    }

    [Fact]
    public void ApplyDetailSetsOnlyThatTab()
    {
        var vm = new ServerViewModel(Node.CloudOnly(Guid.NewGuid(), "cloud"));
        var t = DateTime.UtcNow;
        var procs = new ProcList(false, 10, 1, 1, new[] { new ProcSample(1, null, "p", null, null, 5, 100, null, null, null, null, null) });
        var ports = new PortList(false, t, 100, false, 0, Array.Empty<PortItem>());
        var detail = new LiveDetail(t, procs, ports, null, null, null);

        vm.ApplyDetail(DetailTab.Ports, detail);
        vm.Ports.Should().Be(ports);
        vm.PortsAvailable.Should().BeTrue();
        vm.Procs.Count.Should().Be(0);

        vm.ApplyDetail(DetailTab.Processes, detail with { Ports = null });
        vm.Procs.Count.Should().Be(1);
    }

    [Fact]
    public void ApplyDetailHealthTabEvaluatesHealth()
    {
        var vm = new ServerViewModel(Node.CloudOnly(Guid.NewGuid(), "cloud"));
        var t = DateTime.UtcNow;
        vm.ApplyDetail(DetailTab.Health, new LiveDetail(t, Health: new HealthInfo(Procs: 30_000)));
        vm.Health.Level.Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void MarkReachableFollowsTheEvent()
    {
        var vm = new ServerViewModel(Node.CloudOnly(Guid.NewGuid(), "cloud"));
        vm.MarkReachable(false);
        vm.OfflineReason.Should().NotBeNull();
        vm.MarkReachable(true);
        vm.OfflineReason.Should().BeNull();
        vm.LastError.Should().BeNull();
    }

    [Fact]
    public void IngestSetsHealthScore()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.Ingest(Sample(DateTime.UtcNow, cpu: 10, mem: 8, load: 1.5));
        vm.HealthScore.Level.Should().Be(HealthLevel.Nominal);
        vm.HealthScore.Score.Should().BeGreaterThan(80);
        vm.LoadSeries.Latest.Should().Be(1.5);

        vm.Ingest(Sample(DateTime.UtcNow.AddSeconds(2), cpu: 95, mem: 8, load: 9));
        vm.HealthScore.Worst!.Check.Should().Be(HealthCheck.Cpu);
        vm.HealthScore.Level.Should().Be(HealthLevel.Critical);
        vm.HealthScoreSeries.Points.Select(p => p.Value).Last().Should().Be(vm.HealthScore.Score);
        vm.HealthScore.Level.Should().Be(vm.Health.Level == HealthLevel.Nominal ? HealthScore.LevelOf(vm.HealthScore.Score) : vm.Health.Level);
    }

    private static Sample Sample(DateTime t, double cpu, long mem, double load) => new(1, t,
        new HostInfo("h", "windows", "amd64", "10.0", 0, "s", null),
        new CpuInfo(cpu, load, 0, 0, 8),
        new MemInfo(mem, 100),
        new MemInfo(0, 0),
        null, null, null, null, null, Array.Empty<string>());

    private static Sample Make(DateTime t, long rxCum)
    {
        return new Sample(1, t,
            new HostInfo("h", "windows", "amd64", "10.0", 0, "s", null),
            new CpuInfo(0, 0, 0, 0, 8),
            new MemInfo(1, 2),
            new MemInfo(0, 0),
            null, null,
            new NetInfo(0, 0, rxCum, 0),
            null,
            null,
            Array.Empty<string>());
    }
}
