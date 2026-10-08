using System.Text.Json;
using System.Text.Json.Nodes;
using FluentAssertions;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class AlertRulesTests
{
    public static readonly AlertRules Immediate = new(
        new AlertRule(0, AlertNotify.All), new AlertRule(0, AlertNotify.All),
        new AlertRule(0, AlertNotify.All), new AlertRule(0, AlertNotify.All), 0.8);

    private static readonly DateTime T0 = DateTime.UtcNow;

    // MARK: rules + JSON

    [Fact]
    public void DefaultsMatchSpec()
    {
        var d = AlertRules.Defaults;
        d.Cpu.Should().Be(new AlertRule(300, AlertNotify.Critical));
        d.Mem.Should().Be(new AlertRule(120, AlertNotify.All));
        d.Disk.Should().Be(new AlertRule(0, AlertNotify.All));
        d.Health.Should().Be(new AlertRule(300, AlertNotify.All));
        d.Tolerance.Should().Be(0.8);
    }

    [Fact]
    public void NotifyGates()
    {
        new AlertRule(0, AlertNotify.Off).Allows(HealthLevel.Critical).Should().BeFalse();
        new AlertRule(0, AlertNotify.Critical).Allows(HealthLevel.Warn).Should().BeFalse();
        new AlertRule(0, AlertNotify.Critical).Allows(HealthLevel.Critical).Should().BeTrue();
        new AlertRule(0, AlertNotify.All).Allows(HealthLevel.Warn).Should().BeTrue();
    }

    [Fact]
    public void JsonUsesMacKeysAndRoundTrips()
    {
        var rules = AlertRules.Defaults with { Mem = new AlertRule(60, AlertNotify.Off), Tolerance = 0.9 };
        var json = JsonSerializer.Serialize(rules, SettingsPersistence.JsonOptions);
        var obj = JsonNode.Parse(json)!.AsObject();
        obj["cpu"]!["sustainSeconds"]!.GetValue<int>().Should().Be(300);
        obj["cpu"]!["notify"]!.GetValue<string>().Should().Be("critical");
        obj["mem"]!["notify"]!.GetValue<string>().Should().Be("off");
        obj["tolerance"]!.GetValue<double>().Should().Be(0.9);
        JsonSerializer.Deserialize<AlertRules>(json, SettingsPersistence.JsonOptions).Should().Be(rules);
    }

    [Fact]
    public void JsonMissingKeysUseMetricDefaultsAndClamp()
    {
        var json = """{"cpu":{"notify":"all"},"disk":{"sustainSeconds":99999},"tolerance":0.1}""";
        var r = JsonSerializer.Deserialize<AlertRules>(json, SettingsPersistence.JsonOptions)!;
        r.Cpu.Should().Be(new AlertRule(300, AlertNotify.All));
        r.Mem.Should().Be(AlertRules.DefaultMem);
        r.Disk.SustainSeconds.Should().Be(3600);
        r.Health.Should().Be(AlertRules.DefaultHealth);
        r.Tolerance.Should().Be(0.5);
    }

    [Fact]
    public void SettingsRoundTripAlertsAndCustomAlertsWithoutLegacyKeys()
    {
        var path = Path.GetTempFileName();
        try
        {
            var custom = Immediate with { Tolerance = 1.0 };
            var p = PersistedSettings.Defaults();
            p.Thresholds.Alerts = AlertRules.Defaults with { Cpu = new AlertRule(600, AlertNotify.All) };
            p.Nodes = new() { new Node { DisplayName = "n", Kind = NodeKind.Ssh, CustomAlerts = custom } };
            SettingsPersistence.Save(p, path).Should().BeTrue();

            var root = JsonNode.Parse(File.ReadAllText(path))!.AsObject();
            root["thresholds"]!["alerts"]!["cpu"]!["sustainSeconds"]!.GetValue<int>().Should().Be(600);
            root["thresholds"]!.AsObject().ContainsKey("cpuSustainSamples").Should().BeFalse();
            root["nodes"]![0]!["customAlerts"]!["tolerance"]!.GetValue<double>().Should().Be(1.0);

            var back = SettingsPersistence.Load(path);
            back.Thresholds.Alerts!.Cpu.Should().Be(new AlertRule(600, AlertNotify.All));
            back.Nodes[0].CustomAlerts.Should().Be(custom);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void LegacySampleCountsMigrateToSeconds()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.WriteAllText(path, """
            {"nodes":[{"id":"6f1c0d1e-0000-4000-8000-000000000001","displayName":"n","kind":"ssh",
              "customThresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.5,"memCritical":0.6,"diskWarn":0.5,"diskCritical":0.6,"cpuSustainSamples":1}}],
             "thresholds":{"cpuWarn":0.75,"cpuCritical":0.9,"memWarn":0.75,"memCritical":0.9,"diskWarn":0.85,"diskCritical":0.95,
               "cpuSustainSamples":6,"memSustainSamples":1,"diskSustainSamples":3},
             "sshPollingIntervalSeconds":15}
            """);
            var settings = new ServerSettings(path);
            settings.Alerts.Cpu.Should().Be(new AlertRule(90, AlertNotify.Critical));
            settings.Alerts.Mem.Should().Be(AlertRules.DefaultMem);
            settings.Alerts.Disk.Should().Be(new AlertRule(45, AlertNotify.All));

            var node = SettingsPersistence.Load(path).Nodes[0];
            node.CustomThresholds!.CpuWarn.Should().Be(0.5);
            node.CustomAlerts.Should().BeNull();
            node.EffectiveAlerts(settings.Alerts).Should().Be(settings.Alerts);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ImportWithoutAlertsKeepsCurrentRules()
    {
        var path = Path.GetTempFileName();
        try
        {
            var p = PersistedSettings.Defaults();
            var current = AlertRules.Defaults with { Disk = new AlertRule(60, AlertNotify.Off) };
            p.Thresholds.Alerts = current;
            SettingsPersistence.Save(p, path);

            var export = new SettingsExport { GlobalThresholds = new PersistedThresholds { CpuWarn = 0.5 } };
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, false, false), path);

            var back = SettingsPersistence.Load(path);
            back.Thresholds.CpuWarn.Should().Be(0.5);
            back.Thresholds.Alerts.Should().Be(current);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void NodeOverrideWins()
    {
        var node = new Node { CustomAlerts = Immediate };
        node.EffectiveAlerts(AlertRules.Defaults).Should().Be(Immediate);
        new Node().EffectiveAlerts(AlertRules.Defaults).Should().Be(AlertRules.Defaults);
    }

    // MARK: sustain window

    [Fact]
    public void WindowNeedsFullCoverage()
    {
        var w = new SustainWindow();
        for (int s = 0; s < 300; s += 10)
        {
            w.Record(T0.AddSeconds(s), HealthLevel.Critical, 300);
            w.Level(300, 0.8).Should().Be(HealthLevel.Nominal);
        }
        w.Record(T0.AddSeconds(300), HealthLevel.Critical, 300);
        w.Level(300, 0.8).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void WindowSurvivesOneDip()
    {
        var w = new SustainWindow();
        for (int s = 0; s <= 300; s += 10)
            w.Record(T0.AddSeconds(s), s == 150 ? HealthLevel.Nominal : HealthLevel.Critical, 300);
        w.Level(300, 0.8).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void FreshSpikeAfterCalmDoesNotFire()
    {
        var w = new SustainWindow();
        for (int s = 0; s <= 300; s += 10) w.Record(T0.AddSeconds(s), HealthLevel.Nominal, 300);
        w.Record(T0.AddSeconds(310), HealthLevel.Critical, 300);
        w.Record(T0.AddSeconds(320), HealthLevel.Critical, 300);
        w.Level(300, 0.8).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void CriticalFallsBackToWarnWhenOnlyWarnIsSustained()
    {
        var w = new SustainWindow();
        for (int s = 0; s <= 100; s += 10)
            w.Record(T0.AddSeconds(s), s >= 60 ? HealthLevel.Critical : HealthLevel.Warn, 100);
        w.Level(100, 0.8).Should().Be(HealthLevel.Warn);
    }

    [Fact]
    public void ZeroSustainIsLatestLevel()
    {
        var w = new SustainWindow();
        w.Record(T0, HealthLevel.Critical, 0);
        w.Level(0, 0.8).Should().Be(HealthLevel.Critical);
        w.Record(T0.AddSeconds(1), HealthLevel.Nominal, 0);
        w.Level(0, 0.8).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void LongGapStartsAgain()
    {
        var w = new SustainWindow();
        for (int s = 0; s <= 60; s += 10) w.Record(T0.AddSeconds(s), HealthLevel.Critical, 60);
        w.Level(60, 0.8).Should().Be(HealthLevel.Critical);
        w.Record(T0.AddMinutes(30), HealthLevel.Critical, 60);
        w.Level(60, 0.8).Should().Be(HealthLevel.Nominal);
    }

    // MARK: view model

    [Fact]
    public void CardStaysRawWhileAlertIsGated()
    {
        var vm = Vm(AlertRules.Defaults);
        vm.Ingest(Make(T0, cpu: 95));
        vm.RawLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Critical);
        vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Nominal);
        for (int s = 10; s <= 300; s += 10) vm.Ingest(Make(T0.AddSeconds(s), cpu: 95));
        vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void ResetAfterUnreachableStartsAgain()
    {
        var vm = Vm(AlertRules.Defaults with { Cpu = new AlertRule(60, AlertNotify.All) });
        for (int s = 0; s <= 60; s += 10) vm.Ingest(Make(T0.AddSeconds(s), cpu: 95));
        vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Critical);
        vm.ResetAlerts();
        vm.Ingest(Make(T0.AddSeconds(70), cpu: 95));
        vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void DiskAlertsAtOnceByDefault()
    {
        var vm = Vm(AlertRules.Defaults);
        vm.Ingest(Make(T0, disk: 97));
        vm.AlertLevel(AlertMetric.Disk).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void MemoryUnderPressureIsCriticalAtOnce()
    {
        var vm = Vm(AlertRules.Defaults);
        vm.Ingest(Make(T0, mem: 80, health: new HealthInfo(10, Psi: new PsiInfo(0, 0, 8, 0, 0))));
        vm.MemPressured.Should().BeTrue();
        vm.AlertLevel(AlertMetric.Mem).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void MemoryWithoutPressureOnlyWarns()
    {
        var vm = Vm(AlertRules.Defaults);
        for (int s = 0; s <= 120; s += 10)
            vm.Ingest(Make(T0.AddSeconds(s), mem: 95, health: new HealthInfo(10, MemPressure: 1)));
        vm.MemPressured.Should().BeFalse();
        vm.RawLevel(AlertMetric.Mem).Should().Be(HealthLevel.Critical);
        vm.AlertLevel(AlertMetric.Mem).Should().Be(HealthLevel.Warn);
    }

    [Fact]
    public void MemoryWithUnknownPressureUsesSustain()
    {
        var vm = Vm(AlertRules.Defaults);
        for (int s = 0; s <= 120; s += 10) vm.Ingest(Make(T0.AddSeconds(s), mem: 95));
        vm.MemPressured.Should().BeNull();
        vm.AlertLevel(AlertMetric.Mem).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void SwapGrowthCountsAsPressure()
    {
        var vm = Vm(AlertRules.Defaults);
        vm.Ingest(Make(T0, mem: 80, swapUsed: 0));
        vm.MemPressured.Should().BeNull();
        vm.Ingest(Make(T0.AddSeconds(10), mem: 80, swapUsed: 0));
        vm.MemPressured.Should().BeFalse();
        vm.Ingest(Make(T0.AddSeconds(20), mem: 80, swapUsed: 50L * 1_048_576));
        vm.MemPressured.Should().BeTrue();
        vm.AlertLevel(AlertMetric.Mem).Should().Be(HealthLevel.Critical);
    }

    [Fact]
    public void HealthInodesBypassSustain()
    {
        var vm = Vm(AlertRules.Defaults);
        vm.Ingest(Make(T0, inodes: 0.97, health: new HealthInfo(25_000)));
        vm.Health.TintOf(HealthSignal.Procs).Should().Be(HealthLevel.Critical);
        vm.AlertLevel(AlertMetric.Health).Should().Be(HealthLevel.Critical);

        var procsOnly = Vm(AlertRules.Defaults);
        procsOnly.Ingest(Make(T0, health: new HealthInfo(25_000)));
        procsOnly.AlertLevel(AlertMetric.Health).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void NotifierRespectsRuleAndFiresOnSustainedEscalation()
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
            var node = new Node { DisplayName = "n" };
            nodes.Add(node);
            var vm = Vm(AlertRules.Defaults with { Cpu = new AlertRule(60, AlertNotify.Critical) }, node);

            // Warn sustained: CPU notifies on critical only.
            for (int s = 0; s <= 60; s += 10) { vm.Ingest(Make(T0.AddSeconds(s), cpu: 80)); notifier.Evaluate(vm); }
            vm.AlertLevel(AlertMetric.Cpu).Should().Be(HealthLevel.Warn);
            raised.Should().BeEmpty();

            for (int s = 70; s <= 130; s += 10) { vm.Ingest(Make(T0.AddSeconds(s), cpu: 95)); notifier.Evaluate(vm); }
            raised.Should().ContainSingle().Which.Severity.Should().Be(ThresholdNotifier.Severity.Critical);
        }
        finally { File.Delete(path); }
    }

    private static ServerViewModel Vm(AlertRules rules, Node? node = null)
        => new(node ?? new Node { DisplayName = "n" }) { AlertRules = rules };

    private static Sample Make(DateTime t, double cpu = 10, double mem = 10, double disk = 10,
                               long? swapUsed = null, double? inodes = null, HealthInfo? health = null)
    {
        const long memTotal = 16_000_000_000L, diskTotal = 500_000_000_000L;
        var d = new DiskSample("/", "ext4", (long)(disk / 100 * diskTotal), diskTotal,
            inodes is double f ? (long)(f * 1000) : null, inodes is null ? null : 1000);
        return new Sample(
            1, t,
            new HostInfo("n", "linux", "amd64", "6", 0, "s", null),
            new CpuInfo(cpu, 0, 0, 0, 8),
            new MemInfo((long)(mem / 100 * memTotal), memTotal),
            swapUsed is long su ? new MemInfo(su, 8L * 1_073_741_824) : new MemInfo(0, 0),
            new[] { d },
            null, null, null, null, Array.Empty<string>(), health);
    }
}
