using Towertail.WinUI.State;

namespace Towertail.Tests.Helpers;

/// <summary>Builds a sampler <see cref="Sample"/> for view-model and store tests.</summary>
internal static class SampleFixture
{
    private const long MemTotal = 16_000_000_000L, DiskTotal = 500_000_000_000L;

    /// <param name="diskPct">Null gives no disks.</param>
    /// <param name="rxCum">Null gives no network block.</param>
    /// <param name="inodes">Used fraction of 1000 inodes. Null gives no inode counts.</param>
    internal static Sample Make(DateTime? ts = null, double cpu = 10, double memPct = 25, double? diskPct = 20,
        long? rxCum = null, ProcList? procs = null, long? swapUsed = null, double? inodes = null, HealthInfo? health = null)
    {
        var disks = diskPct is double d
            ? new[] { new DiskSample("/", "ext4", (long)(d / 100 * DiskTotal), DiskTotal,
                inodes is double f ? (long)(f * 1000) : null, inodes is null ? null : 1000) }
            : null;
        return new Sample(
            1, ts ?? DateTime.UtcNow,
            new HostInfo("pc", "linux", "amd64", "6.1", 0, "s", null),
            new CpuInfo(cpu, 0, 0, 0, 8),
            new MemInfo((long)(memPct / 100 * MemTotal), MemTotal),
            swapUsed is long su ? new MemInfo(su, 8L * 1_073_741_824) : new MemInfo(0, 0),
            disks,
            null,
            rxCum is long rx ? new NetInfo(0, 0, rx, 0) : null,
            procs,
            null,
            Array.Empty<string>(),
            health);
    }
}
