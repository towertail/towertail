using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ThresholdOverridesTests
{
    private static readonly MetricThresholds Global = MetricThresholds.Defaults.WithHealth(10, 20, 30, 40);

    [Fact]
    public void AppliedChangesOnlyTheSetMetric()
    {
        var o = new ThresholdOverrides(Mem: new ThresholdPair(0.5, 0.6), Zombies: new ThresholdPair(7, 8), Pids: new ThresholdPair(0.1, 0.2));
        var t = o.Applied(Global);

        (t.MemWarn, t.MemCritical).Should().Be((0.5, 0.6));
        (t.ZombiesWarn, t.ZombiesCritical).Should().Be((7, 8));
        t.HealthOrDefault.Pids.Should().Be(new ThresholdPair(0.1, 0.2));
        (t.CpuWarn, t.CpuCritical).Should().Be((Global.CpuWarn, Global.CpuCritical));
        (t.ProcsWarn, t.ProcsCritical).Should().Be((10, 20));
        t.HealthOrDefault.Files.Should().Be(HealthLimits.Defaults.Files);
    }

    [Fact]
    public void MemPressureOverrideAlsoSetsMacMemPressure()
    {
        var pair = new ThresholdPair(50, 60);
        var h = new ThresholdOverrides(MemPressure: pair).Applied(Global).HealthOrDefault;
        h.MemPressure.Should().Be(pair);
        h.MacMemPressure.Should().Be(pair);
        new ThresholdOverrides().Applied(Global).Should().Be(Global);
    }

    [Fact]
    public void FromLegacyGivesCpuMemAndDisk()
    {
        var legacy = MetricThresholds.Defaults with { CpuWarn = 0.1, CpuCritical = 0.2, MemWarn = 0.3, MemCritical = 0.4, DiskWarn = 0.5, DiskCritical = 0.6 };
        var o = ThresholdOverrides.FromLegacy(legacy);
        o.Cpu.Should().Be(new ThresholdPair(0.1, 0.2));
        o.Mem.Should().Be(new ThresholdPair(0.3, 0.4));
        o.Disk.Should().Be(new ThresholdPair(0.5, 0.6));
        o.Procs.Should().BeNull();
        o.IsEmpty.Should().BeFalse();
        new ThresholdOverrides().IsEmpty.Should().BeTrue();
    }

    [Fact]
    public void WithSetsAndClearsOneMetric()
    {
        var o = new ThresholdOverrides().With(ThresholdMetric.Inodes, new ThresholdPair(0.4, 0.5));
        o.Get(ThresholdMetric.Inodes).Should().Be(new ThresholdPair(0.4, 0.5));
        o.With(ThresholdMetric.Inodes, null).IsEmpty.Should().BeTrue();
    }
}
