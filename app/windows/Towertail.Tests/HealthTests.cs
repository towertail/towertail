using FluentAssertions;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class HealthTests
{
    private const string Head = """{"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]""";

    private static readonly MetricThresholds T = MetricThresholds.Defaults;

    [Fact]
    public void DecodesHealthSkippedAndInodes()
    {
        var json = Head + """
            ,"health":{"procs":34983,"zombies":33817,"zombie_parents":[{"pid":12,"name":"firebolt","count":33800}],
              "pids_used":30000,"pids_max":32768,"files_used":100,"files_max":1000,
              "psi":{"cpu_some":1.5,"mem_some":12,"mem_full":3,"io_some":30,"io_full":22},"mem_pressure":2},
             "procs":{"root":false,"top_n":25,"total":34983,"visible":34983,"items":[],"skipped":true},
             "disks":[{"mount":"/data","fs":"ext4","used":1,"total":2,"inodes_used":90,"inodes_total":100}]}
            """;
        var s = SampleCodec.Decode(json);

        s.Health.Should().NotBeNull();
        var h = s.Health!;
        h.Procs.Should().Be(34983);
        h.Zombies.Should().Be(33817);
        h.ZombieParents.Should().ContainSingle().Which.Should().Be(new ZombieParent(12, "firebolt", 33800));
        h.PidsUsed.Should().Be(30000);
        h.PidsMax.Should().Be(32768);
        h.FilesUsed.Should().Be(100);
        h.FilesMax.Should().Be(1000);
        h.Psi!.MemSome.Should().Be(12);
        h.Psi.IoFull.Should().Be(22);
        h.MemPressure.Should().Be(2);
        s.Procs!.Skipped.Should().BeTrue();
        s.Disks![0].InodesUsed.Should().Be(90);
        s.Disks[0].InodesTotal.Should().Be(100);
    }

    [Fact]
    public void DecodesWithoutHealthFields()
    {
        var json = Head + """
            ,"procs":{"root":false,"top_n":25,"total":10,"visible":10,"items":[]},
             "disks":[{"mount":"/","fs":"ext4","used":1,"total":2}]}
            """;
        var s = SampleCodec.Decode(json);
        s.Health.Should().BeNull();
        s.Procs!.Skipped.Should().BeNull();
        s.Disks![0].InodesUsed.Should().BeNull();
        s.Disks[0].InodesTotal.Should().BeNull();
    }

    [Fact]
    public void DecodesMinimalHealth()
    {
        var s = SampleCodec.Decode(Head + ""","health":{"procs":42}}""");
        s.Health.Should().Be(new HealthInfo(42, null, null, null, null, null, null, null, null));
    }

    [Fact]
    public void ThresholdsMissingHealthKeysLoadAsDefaults()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.WriteAllText(path, """{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.7,"memCritical":0.8,"diskWarn":0.8,"diskCritical":0.9}}""");
            var t = new ServerSettings(path).Thresholds;
            t.CpuWarn.Should().Be(0.5);
            t.ProcsWarn.Should().Be(5000);
            t.ProcsCritical.Should().Be(20000);
            t.ZombiesWarn.Should().Be(200);
            t.ZombiesCritical.Should().Be(2000);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ThresholdsHealthRoundTripThroughPersist()
    {
        var path = Path.GetTempFileName();
        try
        {
            var s = new ServerSettings(path);
            s.Thresholds = s.Thresholds.WithHealth(100, 200, 3, 4);
            s.Persist();
            var t = new ServerSettings(path).Thresholds;
            (t.ProcsWarn, t.ProcsCritical, t.ZombiesWarn, t.ZombiesCritical).Should().Be((100, 200, 3, 4));
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void WithHealthClamps()
    {
        var t = T.WithHealth(0, -5, 500, 100);
        t.ProcsWarn.Should().Be(1);
        t.ProcsCritical.Should().Be(1);
        t.ZombiesWarn.Should().Be(500);
        t.ZombiesCritical.Should().Be(500);
    }

    [Fact]
    public void EffectiveTakesHealthFromGlobal()
    {
        var global = T.WithHealth(10, 20, 30, 40);
        var custom = T with { CpuWarn = 0.1 };
        var e = MetricThresholds.Effective(global, custom);
        e.CpuWarn.Should().Be(0.1);
        (e.ProcsWarn, e.ProcsCritical, e.ZombiesWarn, e.ZombiesCritical).Should().Be((10, 20, 30, 40));
        MetricThresholds.Effective(global, null).Should().Be(global);
    }

    private static HealthInfo H(int procs = 100, int? zombies = null, IReadOnlyList<ZombieParent>? parents = null,
        long? pidsUsed = null, long? pidsMax = null, long? filesUsed = null, long? filesMax = null,
        PsiInfo? psi = null, int? memPressure = null)
        => new(procs, zombies, parents, pidsUsed, pidsMax, filesUsed, filesMax, psi, memPressure);

    private static HealthStatus Eval(HealthInfo? h, params DiskSample[] disks) => HealthStatus.Evaluate(h, disks, T);

    [Fact]
    public void NominalWhenNoHealth()
    {
        var st = Eval(null);
        st.Level.Should().Be(HealthLevel.Nominal);
        st.Reasons.Should().BeEmpty();
        st.Summary.Should().BeNull();
    }

    [Theory]
    [InlineData(4999, HealthLevel.Nominal)]
    [InlineData(5000, HealthLevel.Warn)]
    [InlineData(20000, HealthLevel.Critical)]
    public void ProcsRule(int procs, HealthLevel level)
        => Eval(H(procs)).Level.Should().Be(level);

    [Fact]
    public void ProcsReasonFormat()
        => Eval(H(34983)).Reasons.Single().Text.Should().Be("34,983 processes");

    [Fact]
    public void ZombiesRuleNamesTopParent()
    {
        var st = Eval(H(zombies: 33817, parents: new[] { new ZombieParent(1, "firebolt", 33000) }));
        st.Reasons.Should().ContainSingle(r => r.Signal == HealthSignal.Zombies)
            .Which.Should().Be(new HealthReason(HealthSignal.Zombies, HealthLevel.Critical, "33,817 zombies (firebolt)"));
        Eval(H(zombies: 200)).Level.Should().Be(HealthLevel.Warn);
        Eval(H(zombies: 199)).Level.Should().Be(HealthLevel.Nominal);
    }

    [Theory]
    [InlineData(69, HealthLevel.Nominal)]
    [InlineData(70, HealthLevel.Warn)]
    [InlineData(92, HealthLevel.Critical)]
    public void PidsRule(long used, HealthLevel level)
    {
        var st = Eval(H(pidsUsed: used, pidsMax: 100));
        st.Level.Should().Be(level);
        if (level == HealthLevel.Critical) st.Reasons.Single().Text.Should().Be("PIDs 92% of limit");
    }

    [Theory]
    [InlineData(79, HealthLevel.Nominal)]
    [InlineData(85, HealthLevel.Warn)]
    [InlineData(95, HealthLevel.Critical)]
    public void FilesRule(long used, HealthLevel level)
    {
        var st = Eval(H(filesUsed: used, filesMax: 100));
        st.Level.Should().Be(level);
        if (level == HealthLevel.Warn) st.Reasons.Single().Text.Should().Be("Open files 85% of limit");
    }

    [Fact]
    public void ZeroMaxIsIgnored()
        => Eval(H(pidsUsed: 5, pidsMax: 0, filesUsed: 5, filesMax: 0)).Level.Should().Be(HealthLevel.Nominal);

    [Theory]
    [InlineData(84, HealthLevel.Nominal)]
    [InlineData(90, HealthLevel.Warn)]
    [InlineData(95, HealthLevel.Critical)]
    public void InodesRule(long used, HealthLevel level)
    {
        var st = Eval(H(), new DiskSample("/data", "ext4", 1, 2, used, 100));
        st.Level.Should().Be(level);
        if (level == HealthLevel.Warn) st.Reasons.Single().Text.Should().Be("Inodes 90% on /data");
    }

    [Fact]
    public void InodesCheckedWithoutHealthBlock()
        => Eval(null, new DiskSample("/", "ext4", 1, 2, 96, 100)).Level.Should().Be(HealthLevel.Critical);

    [Fact]
    public void MemoryPsiRule()
    {
        Eval(H(psi: new PsiInfo(0, 9.9, 0, 0, 0))).Level.Should().Be(HealthLevel.Nominal);
        var warn = Eval(H(psi: new PsiInfo(0, 12, 5, 0, 0)));
        warn.Reasons.Single().Should().Be(new HealthReason(HealthSignal.MemPressure, HealthLevel.Warn, "Memory pressure 12%"));
        var crit = Eval(H(psi: new PsiInfo(0, 50, 20, 0, 0)));
        crit.Reasons.Single().Level.Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void IoPsiRule()
    {
        Eval(H(psi: new PsiInfo(0, 0, 0, 90, 19))).Level.Should().Be(HealthLevel.Nominal);
        Eval(H(psi: new PsiInfo(0, 0, 0, 0, 20))).Level.Should().Be(HealthLevel.Warn);
        var crit = Eval(H(psi: new PsiInfo(0, 0, 0, 0, 66)));
        crit.Reasons.Single().Should().Be(new HealthReason(HealthSignal.IoPressure, HealthLevel.Critical, "I/O pressure 66%"));
    }

    [Theory]
    [InlineData(1, HealthLevel.Nominal)]
    [InlineData(2, HealthLevel.Warn)]
    [InlineData(4, HealthLevel.Critical)]
    public void MacMemPressureRule(int mp, HealthLevel level)
        => Eval(H(memPressure: mp)).Level.Should().Be(level);

    [Fact]
    public void CriticalReasonsComeFirst()
    {
        var st = Eval(H(6000, pidsUsed: 95, pidsMax: 100, filesUsed: 85, filesMax: 100));
        st.Level.Should().Be(HealthLevel.Critical);
        st.Reasons.Select(r => r.Text).Should().Equal("PIDs 95% of limit", "6,000 processes", "Open files 85% of limit");
        st.Summary.Should().Be("PIDs 95% of limit +2 more");
        st.Body.Should().Be("PIDs 95% of limit · 6,000 processes · Open files 85% of limit");
        st.TintOf(HealthSignal.Pids).Should().Be(HealthLevel.Critical);
        st.TintOf(HealthSignal.Files).Should().Be(HealthLevel.Warn);
        st.TintOf(HealthSignal.Zombies).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void CustomThresholdsApply()
        => HealthStatus.Evaluate(H(150), null, T.WithHealth(100, 1000, 1, 2)).Level.Should().Be(HealthLevel.Warn);

    [Fact]
    public void ViewModelSkipsEmptySnapshotAndKeepsTotal()
    {
        var vm = new ServerViewModel(new Node { DisplayName = "n", Kind = NodeKind.Local });
        var json = Head + ""","health":{"procs":30000},"procs":{"root":false,"top_n":25,"total":30000,"visible":30000,"items":[],"skipped":true}}""";
        // Fresh timestamp: MetricSeries drops points past its retention window.
        vm.Ingest(SampleCodec.Decode(json.Replace("2026-04-20T12:00:00Z", DateTime.UtcNow.ToString("o"))));
        vm.ProcsSkippedTotal.Should().Be(30000);
        vm.Procs.Latest.Should().BeNull();
        vm.ProcCountSeries.Points.Should().ContainSingle().Which.Value.Should().Be(30000);
        vm.Health.Level.Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void NotifierFiresOnHealthEscalationWithBody()
    {
        var path = Path.GetTempFileName();
        try
        {
            var nodes = new NodeStore(path);
            var settings = new ServerSettings(path)
            {
                NotificationsEnabled = true, NotifyWarn = true, NotifyCritical = true, NotifyDebounceSeconds = 0,
            };
            var notifier = new ThresholdNotifier(settings, nodes);
            var raised = new List<ThresholdNotifier.NotifyPayload>();
            notifier.Raised += (_, p) => raised.Add(p);
            var node = new Node { DisplayName = "n", Kind = NodeKind.Local };
            nodes.Add(node);
            var vm = new ServerViewModel(node);

            vm.Ingest(SampleCodec.Decode(Head + ""","health":{"procs":6000}}"""));
            notifier.Evaluate(vm);
            vm.Ingest(SampleCodec.Decode(Head + ""","health":{"procs":25000,"zombies":300}}"""));
            notifier.Evaluate(vm);

            var health = raised.Where(p => p.Metric == ThresholdNotifier.Metric.Health).ToList();
            health.Select(p => p.Severity).Should().Equal(ThresholdNotifier.Severity.Warn, ThresholdNotifier.Severity.Critical);
            health[1].Body.Should().Be("25,000 processes · 300 zombies");
        }
        finally { File.Delete(path); }
    }
}
