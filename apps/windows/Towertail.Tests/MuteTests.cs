using System.Text.Json;
using System.Text.Json.Nodes;
using FluentAssertions;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class MuteTests
{
    private const string Head = """{"v":1,"ts":"TS","host":{"name":"h","os":"darwin","arch":"arm64","kernel":"24","uptime_s":1,"sampler":"x"},"cpu":{"pct":CPU,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[],"health":{"procs":PROCS,"mem_pressure":1,"mem_pressure_pct":PCT}}""";

    private static Sample Make(double cpu = 10, int pct = 50, int procs = 10, DateTime? ts = null)
        => SampleCodec.Decode(Head.Replace("TS", (ts ?? DateTime.UtcNow).ToString("O")).Replace("CPU", $"{cpu}")
            .Replace("PCT", $"{pct}").Replace("PROCS", $"{procs}"));

    private static MutedSignals M(params AlertSignal[] s) => new(s);

    [Fact]
    public void JsonUsesMacNamesSortedAndSkipsUnknown()
    {
        JsonSerializer.Serialize(M(AlertSignal.MemPressure, AlertSignal.Cpu, AlertSignal.IoPressure))
            .Should().Be("""["cpu","ioPressure","memPressure"]""");
        JsonSerializer.Deserialize<MutedSignals>("""["zombies","future","Cpu"]""")
            .Should().Be(M(AlertSignal.Zombies));
        JsonSerializer.Deserialize<MutedSignals>("\"cpu\"").Should().Be(MutedSignals.None);
    }

    [Fact]
    public void EqualityComparesTheSetAndEffectiveIsUnion()
    {
        M(AlertSignal.Cpu, AlertSignal.Disk).Should().Be(M(AlertSignal.Disk, AlertSignal.Cpu));
        MutedSignals.Effective(M(AlertSignal.Cpu), M(AlertSignal.Zombies))
            .Should().Be(M(AlertSignal.Cpu, AlertSignal.Zombies));
    }

    [Fact]
    public void MutedCpuKeepsRawLevelButNoAlert()
    {
        var vm = new ServerViewModel(new Node { DisplayName = "n" }) { AlertRules = AlertRulesTests.Immediate };
        vm.Muted = M(AlertSignal.Cpu);
        vm.Ingest(Make(cpu: 97));
        vm.RawLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Critical);
        vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void MutedHealthSignalSkipsHostAlertLevel()
    {
        var vm = new ServerViewModel(new Node { DisplayName = "n" }) { AlertRules = AlertRulesTests.Immediate };
        vm.Ingest(Make(pct: 95));
        vm.HostAlertLevel().Should().Be(HealthLevel.Critical);

        vm.Muted = M(AlertSignal.MemPressure);
        vm.Ingest(Make(pct: 95));
        vm.Health.Level.Should().Be(HealthLevel.Critical);
        vm.HostAlertLevel().Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void SameMutesDoNotResetHealthWindow()
    {
        var rules = AlertRulesTests.Immediate with { Health = new AlertRule(1, AlertNotify.All) };
        var vm = new ServerViewModel(new Node { DisplayName = "n" }) { AlertRules = rules };
        var t0 = DateTime.UtcNow.AddSeconds(-10);
        vm.Muted = M(AlertSignal.Cpu);
        vm.Ingest(Make(pct: 95, ts: t0));
        vm.Muted = M(AlertSignal.Cpu);
        vm.Ingest(Make(pct: 95, ts: t0.AddSeconds(2)));
        vm.AlertLevel(AlertMetric.Health).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void NotifierSkipsMutedMetricAndListsOnlyUnmutedReasons()
    {
        var path = Path.GetTempFileName();
        try
        {
            var nodes = new NodeStore(path);
            var settings = new ServerSettings(path) { NotificationsEnabled = true, NotifyDebounceSeconds = 0 };
            var notifier = new ThresholdNotifier(settings, nodes);
            var bodies = new List<string>();
            notifier.Raised += (_, p) => bodies.Add(p.Body ?? "");
            var node = new Node { DisplayName = "n" };
            nodes.Add(node);
            var vm = new ServerViewModel(node) { AlertRules = AlertRulesTests.Immediate };

            vm.Muted = M(AlertSignal.Cpu);
            vm.Ingest(Make(cpu: 97, pct: 50));
            notifier.Evaluate(vm);
            bodies.Should().BeEmpty();

            vm.Muted = M(AlertSignal.Cpu, AlertSignal.MemPressure);
            vm.Ingest(Make(cpu: 97, pct: 95, procs: 6000));
            notifier.Evaluate(vm);
            bodies.Should().Equal("6,000 processes");
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void NodeMutedSignalsRoundTripAndOldFileDecodesEmpty()
    {
        var path = Path.GetTempFileName();
        try
        {
            var store = new NodeStore(path);
            var n = new Node { DisplayName = "db", MutedSignals = M(AlertSignal.MemPressure) };
            store.Add(n);
            new NodeStore(path).ById(n.Id)!.MutedSignals.Should().Be(M(AlertSignal.MemPressure));

            var old = JsonSerializer.Deserialize<Node>("""{"displayName":"x"}""", SettingsPersistence.JsonOptions)!;
            old.MutedSignals.Should().Be(MutedSignals.None);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void BadMutedValueKeepsSettings()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.WriteAllText(path, """{"thresholds":{"cpuWarn":0.5,"muted":{"x":1}},"nodes":[{"displayName":"db","mutedSignals":7}]}""");
            var p = SettingsPersistence.Load(path);
            p.Thresholds.CpuWarn.Should().Be(0.5);
            p.Thresholds.Muted.Should().Be(MutedSignals.None);
            p.Nodes.Single().DisplayName.Should().Be("db");
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void MacExportMutesImportOnWindows()
    {
        var path = Path.GetTempFileName();
        try
        {
            var id = Guid.NewGuid();
            var export = SettingsTransfer.Decode("""
                {"version":1,"sourcePlatform":"darwin",
                 "globalThresholds":{"cpuWarn":0.75,"cpuCritical":0.9,"memWarn":0.75,"memCritical":0.9,"diskWarn":0.85,"diskCritical":0.95,
                   "muted":["zombies"],"health":{"macMemPressure":{"warn":80,"critical":90}}},
                 "nodes":[{"id":"ID","displayName":"mba","kind":"ssh","mutedSignals":["memPressure"]}]}
                """.Replace("ID", id.ToString()));
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, true, true), path);
            var p = SettingsPersistence.Load(path);
            p.Thresholds.Muted.Should().Be(M(AlertSignal.Zombies));
            p.Nodes.Single(n => n.Id == id).MutedSignals.Should().Be(M(AlertSignal.MemPressure));
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ImportWithoutMutedKeepsCurrentMutes()
    {
        var path = Path.GetTempFileName();
        try
        {
            new ServerSettings(path) { MutedSignals = M(AlertSignal.Disk) }.Persist();
            var export = SettingsTransfer.BuildFromCurrent(path);
            export.GlobalThresholds.Muted = null;
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, false, false), path);
            new ServerSettings(path).MutedSignals.Should().Be(M(AlertSignal.Disk));
        }
        finally { File.Delete(path); }
    }
}
