using FluentAssertions;
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
