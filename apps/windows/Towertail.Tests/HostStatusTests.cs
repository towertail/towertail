using FluentAssertions;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class HostStatusTests
{
    private static readonly Node Cloud = Node.CloudOnly(Guid.NewGuid(), "db");
    private static readonly Node Ssh = new() { DisplayName = "web", Kind = NodeKind.Ssh };

    private static Sample SampleAt(DateTime ts) => new(
        1, ts, new HostInfo("h", "linux", "amd64", "6", 0, "s", null), new CpuInfo(1, 0, 0, 0, 1),
        new MemInfo(1, 2), new MemInfo(0, 0), [], null, null, null, null, []);

    [Fact]
    public void DisabledHostsSayDisabled()
    {
        HostStatus.For(Ssh with { Enabled = false }, null, true, false).Should().Be(new HostStatus("disabled", HostStatusLevel.Disabled));
    }

    [Fact]
    public void ACloudHostWithNoSummaryTakesTheFeedState()
    {
        var vm = new ServerViewModel(Cloud);

        HostStatus.For(Cloud, vm, true, false).Text.Should().Be("online");
        HostStatus.For(Cloud, vm, true, true).Text.Should().Be("offline");
        HostStatus.For(Cloud, vm, false, false).Should().Be(new HostStatus("connecting", HostStatusLevel.Pending));
    }

    [Fact]
    public void ASshHostWithNoDataIsPending()
    {
        HostStatus.For(Ssh, new ServerViewModel(Ssh), true, false).Level.Should().Be(HostStatusLevel.Pending);
    }

    [Fact]
    public void AHostThatReportedIsOnline()
    {
        var vm = new ServerViewModel(Cloud);
        vm.Ingest(SampleAt(DateTime.UtcNow));

        HostStatus.For(Cloud, vm, false, false).Should().Be(new HostStatus("online", HostStatusLevel.Online));
    }

    [Fact]
    public void AnAlertMakesACloudHostOfflineEvenWithAnOldSummary()
    {
        var vm = new ServerViewModel(Cloud);
        vm.Ingest(SampleAt(DateTime.UtcNow));

        HostStatus.For(Cloud, vm, true, true).Should().Be(new HostStatus("offline", HostStatusLevel.Offline));
    }

    [Fact]
    public void AnAlertDoesNotChangeALocalSshHost()
    {
        HostStatus.For(Ssh, new ServerViewModel(Ssh), true, true).Level.Should().Be(HostStatusLevel.Pending);
    }

    [Fact]
    public void AnOfflineReasonWins()
    {
        var vm = new ServerViewModel(Ssh);
        vm.Ingest(SampleAt(DateTime.UtcNow));
        vm.MarkOffline("timeout");

        HostStatus.For(Ssh, vm, true, false).Should().Be(new HostStatus("offline", HostStatusLevel.Offline));
    }

    [Fact]
    public void ANodeWithNoModelShowsADash()
    {
        HostStatus.For(Cloud, null, true, false).Text.Should().Be("—");
    }
}
