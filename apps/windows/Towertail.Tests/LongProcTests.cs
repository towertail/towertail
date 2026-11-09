using System.Text.Json;
using System.Text.Json.Nodes;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

// The same tracker cases as LongProcTrackerTests in the Mac app and longproc_test.go in the server.
public sealed class LongProcTrackerTests
{
    internal static readonly DateTime T0 = DateTimeOffset.FromUnixTimeSeconds(1_800_000_000).UtcDateTime;
    internal static readonly DateTime Started = DateTimeOffset.FromUnixTimeSeconds(1_799_000_000).UtcDateTime;
    internal static readonly LongProcRule Rule = LongProcRule.Defaults;
    private static readonly IReadOnlySet<string> None = new HashSet<string>();

    internal static ProcSample P(double cpu, int pid, string name, DateTime? start)
        => new(pid, 1, name, "/usr/bin/" + name, "me", cpu, 1_000_000, 4, "R", start, null, null);

    internal static ProcSample Orb(double cpu) => P(cpu, 812, "OrbStack Helper", Started);

    internal static DateTime At(int m) => T0.AddMinutes(m);

    /// <summary>Sends one sample each minute from..through and gives the result of the last one.</summary>
    private static IReadOnlyList<LongProc> Feed(LongProcTracker t, int from, int through, Func<int, ProcSample[]> items,
        LongProcRule? rule = null, IReadOnlySet<string>? ignored = null)
    {
        IReadOnlyList<LongProc> last = Array.Empty<LongProc>();
        for (var m = from; m <= through; m++)
            last = t.Observe(items(m), false, At(m), rule ?? Rule, ignored ?? None);
        return last;
    }

    [Fact]
    public void SteadyHighFlagsAfterSustain()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 29, _ => new[] { Orb(60) }).Should().BeEmpty();
        var f = Feed(t, 30, 30, _ => new[] { Orb(60) });
        f.Should().ContainSingle();
        f[0].Pid.Should().Be(812);
        f[0].HighSince.Should().Be(T0);
        HealthStatus.LongProcText(f[0], At(30)).Should().Be("OrbStack Helper 60% for 30m");
    }

    [Fact]
    public void TwentyPercentDipsStayFlagged()
    {
        var t = new LongProcTracker();
        var f = Feed(t, 0, 90, m => new[] { Orb(m % 5 == 4 ? 10 : 60) });
        f.Should().ContainSingle();
        f[0].HighSince.Should().Be(T0);
    }

    [Fact]
    public void ThirtyPercentDipsDoNotFlag()
    {
        var t = new LongProcTracker();
        for (var m = 0; m <= 120; m++)
            t.Observe(new[] { Orb(m % 10 >= 7 ? 10 : 60) }, false, At(m), Rule, None).Should().BeEmpty();
    }

    [Fact]
    public void BelowLimitNeverFlags()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 120, _ => new[] { Orb(40) }).Should().BeEmpty();
        t.TrackedCount.Should().Be(0);
    }

    [Fact]
    public void ReusedPidWithNewStartIsNewRun()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 20, _ => new[] { Orb(60) });
        var other = Started.AddSeconds(500);
        Feed(t, 21, 40, _ => new[] { P(60, 812, "OrbStack Helper", other) }).Should().BeEmpty();
        var f = Feed(t, 41, 51, _ => new[] { P(60, 812, "OrbStack Helper", other) });
        f.Should().ContainSingle();
        f[0].HighSince.Should().Be(At(21));
    }

    [Fact]
    public void ProcessThatLeavesClears()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) }).Should().ContainSingle();
        Feed(t, 31, 31, _ => new[] { P(1, 1, "other", Started) }).Should().BeEmpty();
        Feed(t, 32, 70, _ => new[] { P(1, 1, "other", Started) });
        t.TrackedCount.Should().Be(0);
    }

    [Fact]
    public void ShortAbsenceKeepsTheRun()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) });
        Feed(t, 31, 32, _ => new[] { P(1, 1, "other", Started) });
        var f = Feed(t, 33, 33, _ => new[] { Orb(60) });
        f.Should().ContainSingle();
        f[0].HighSince.Should().Be(T0);
    }

    [Fact]
    public void IgnoredNamesNeverFlag()
    {
        var t = new LongProcTracker();
        var ignored = Rule.IgnoreSet(new[] { "ORBSTACK helper" });
        Feed(t, 0, 40, _ => new[] { Orb(90) }, ignored: ignored).Should().BeEmpty();
        t.TrackedCount.Should().Be(0);
    }

    [Fact]
    public void IgnoreRemovesATrackedProcess()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) });
        Feed(t, 31, 31, _ => new[] { Orb(60) }, ignored: new HashSet<string> { "orbstack helper" }).Should().BeEmpty();
        t.TrackedCount.Should().Be(0);
    }

    [Fact]
    public void SteadyDayKeepsFewChangePoints()
    {
        var t = new LongProcTracker();
        var day = Rule with { SustainSeconds = 86_400 };
        var f = Feed(t, 0, 24 * 60, m => new[] { Orb(m % 30 == 15 ? 10 : 60), P(70, 2, "node", Started) }, day);
        f.Should().HaveCount(2);
        t.ChangePointCount.Should().BeLessThan(200);
        Feed(t, 1441, 1441, _ => new[] { Orb(60) });
        t.ChangePointCount.Should().BeLessThan(100);
    }

    [Fact]
    public void DisabledRuleClears()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) });
        t.Observe(new[] { Orb(60) }, false, At(31), Rule with { Enabled = false }, None).Should().BeEmpty();
        t.TrackedCount.Should().Be(0);
    }

    [Fact]
    public void SkippedSampleHoldsTheLevel()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) });
        t.Observe(Array.Empty<ProcSample>(), true, At(31), Rule, None).Should().ContainSingle();
        t.Observe(null, false, At(32), Rule, None).Should().ContainSingle();
    }

    [Fact]
    public void SortedByCpu()
    {
        var t = new LongProcTracker();
        var f = Feed(t, 0, 30, _ => new[] { Orb(60), P(90, 2, "node", Started) });
        f.Select(p => p.Name).Should().Equal("node", "OrbStack Helper");
    }

    [Fact]
    public void KeyWithoutStartUsesName()
    {
        LongProcKey.Of(P(1, 5, "a", null)).Should().NotBe(LongProcKey.Of(P(1, 5, "b", null)));
        LongProcKey.Of(Orb(1)).Should().Be(LongProcKey.Of(P(99, 812, "renamed", Started)));
    }

    [Fact]
    public void TimeBackwardResets()
    {
        var t = new LongProcTracker();
        Feed(t, 0, 30, _ => new[] { Orb(60) });
        t.Observe(new[] { Orb(60) }, false, At(-5), Rule, None).Should().BeEmpty();
    }

    [Fact]
    public void ShortNameCutsAt24()
    {
        new LongProc(default, 7, "com.apple.WebKit.WebContent.Extra", null, 1, T0, null).ShortName.Should().Be("com.apple.WebKit.WebCon…");
        new LongProc(default, 7, "", null, 1, T0, null).ShortName.Should().Be("pid 7");
    }
}

public sealed class LongProcRuleTests
{
    private static LongProcRule Decode(string json) => JsonSerializer.Deserialize<LongProcRule>(json, SettingsPersistence.JsonOptions)!;

    [Fact]
    public void DefaultsMatchTheMac()
    {
        var d = LongProcRule.Defaults;
        d.Enabled.Should().BeTrue();
        d.CpuPct.Should().Be(50);
        d.SustainSeconds.Should().Be(1800);
        d.Notify.Should().Be(AlertNotify.All);
        d.Ignore.Should().BeEmpty();
    }

    [Fact]
    public void EachFieldDecodesOnItsOwn()
    {
        var r = Decode("""{"enabled":false,"cpuPct":"x","sustainSeconds":120,"notify":"bad","ignore":["a",1,"B"]}""");
        r.Enabled.Should().BeFalse();
        r.CpuPct.Should().Be(50);
        r.SustainSeconds.Should().Be(120);
        r.Notify.Should().Be(AlertNotify.All);
        r.Ignore.Should().Equal("a", "B");
    }

    [Fact]
    public void ValuesAreClamped()
    {
        var r = Decode("""{"cpuPct":5,"sustainSeconds":999999}""");
        r.CpuPct.Should().Be(10);
        r.SustainSeconds.Should().Be(86_400);
        Decode("""{"cpuPct":5000,"sustainSeconds":1}""").Should().Be(LongProcRule.Defaults with { CpuPct = 800, SustainSeconds = 60 });
    }

    [Fact]
    public void NotAnObjectGivesDefaults() => Decode("[1,2]").Should().Be(LongProcRule.Defaults);

    [Fact]
    public void RoundTripUsesTheMacKeys()
    {
        var r = LongProcRule.Defaults with { Notify = AlertNotify.Off, Ignore = new[] { "x" } };
        var json = JsonSerializer.Serialize(r, SettingsPersistence.JsonOptions);
        var o = JsonNode.Parse(json)!.AsObject();
        o.Select(kv => kv.Key).Should().Equal("enabled", "cpuPct", "sustainSeconds", "notify", "ignore");
        o["notify"]!.GetValue<string>().Should().Be("off");
        Decode(json).Should().Be(r);
    }

    [Fact]
    public void EqualityComparesIgnoreLists()
    {
        (LongProcRule.Defaults with { Ignore = new[] { "a" } }).Should().Be(LongProcRule.Defaults with { Ignore = new List<string> { "a" } });
        (LongProcRule.Defaults with { Ignore = new[] { "a" } }).Should().NotBe(LongProcRule.Defaults);
    }

    [Fact]
    public void IgnoreSetIsLowercaseUnion()
        => (LongProcRule.Defaults with { Ignore = new[] { "Xcode" } }).IgnoreSet(new[] { "Node" }, new[] { "mds" })
            .Should().BeEquivalentTo("xcode", "node", "mds");
}

public sealed class LongProcViewModelTests
{
    private static readonly DateTime T0 = LongProcTrackerTests.T0;

    private static Sample Make(DateTime ts, params ProcSample[] procs) => new(
        1, ts,
        new HostInfo("pc", "linux", "amd64", "6.1", 0, "s", null),
        new CpuInfo(10, 0, 0, 0, 8),
        new MemInfo(4_000_000_000, 16_000_000_000),
        new MemInfo(0, 0),
        new[] { new DiskSample("/", "ext4", 100_000_000_000, 500_000_000_000) },
        null, null,
        new ProcList(false, 10, procs.Length, procs.Length, procs),
        null, Array.Empty<string>());

    private static ServerViewModel Run(Node node, LongProcRule global, int minutes, double cpu = 60)
    {
        var vm = new ServerViewModel(node);
        vm.ApplyLongProc(node, global);
        for (var m = 0; m <= minutes; m++)
            vm.Ingest(Make(T0.AddMinutes(m), LongProcTrackerTests.Orb(cpu)), T0.AddMinutes(m));
        return vm;
    }

    [Fact]
    public void FlaggedProcessWarnsTheHost()
    {
        var vm = Run(Node.LocalWindows("pc"), LongProcRule.Defaults, 30);
        vm.LongProcs.Should().ContainSingle();
        vm.Health.Reasons.Should().ContainSingle(r => r.Signal == HealthSignal.LongProc && r.Text == "OrbStack Helper 60% for 30m");
        vm.HostAlertLevel().Should().Be(HealthLevel.Warn);
        // The health sustain window does not count the long process.
        vm.Health.AlertWindowLevel.Should().Be(HealthLevel.Nominal);
        vm.AlertLevel(AlertMetric.Health).Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void NotifyOffKeepsTheHostNominal()
    {
        var vm = Run(Node.LocalWindows("pc"), LongProcRule.Defaults with { Notify = AlertNotify.Off }, 30);
        vm.LongProcs.Should().ContainSingle();
        vm.HostAlertLevel().Should().Be(HealthLevel.Nominal);
    }

    [Fact]
    public void NodeRuleAndIgnoreListsApply()
    {
        var custom = Node.LocalWindows("pc") with { CustomLongProc = LongProcRule.Defaults with { SustainSeconds = 600 } };
        Run(custom, LongProcRule.Defaults, 10).LongProcs.Should().ContainSingle();

        var vm = Run(Node.LocalWindows("pc"), LongProcRule.Defaults, 30);
        var ignoring = vm.Node with { IgnoredProcs = new[] { "orbstack HELPER" } };
        vm.ApplyLongProc(ignoring, LongProcRule.Defaults).Should().BeTrue();
        vm.LongProcs.Should().BeEmpty();
        vm.ApplyLongProc(ignoring, LongProcRule.Defaults).Should().BeFalse();

        // The global list applies also under a custom rule.
        var vm2 = Run(custom, LongProcRule.Defaults, 10);
        vm2.ApplyLongProc(custom, LongProcRule.Defaults with { Ignore = new[] { "OrbStack Helper" } });
        vm2.LongProcs.Should().BeEmpty();
    }

    [Fact]
    public void HydrateReplaysTheHistory()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.ApplyLongProc(vm.Node, LongProcRule.Defaults);
        var rows = Enumerable.Range(0, 31)
            .Select(m => new HistoryStore.ProcHistoryRow(T0.AddMinutes(m), false, new[] { LongProcTrackerTests.Orb(60) }))
            .ToList();
        vm.HydrateProcs(rows);
        vm.LongProcs.Should().ContainSingle();
        vm.Procs.Hydrated.Should().BeTrue();
    }

    [Fact]
    public void OfflineClears()
    {
        var vm = Run(Node.LocalWindows("pc"), LongProcRule.Defaults, 30);
        vm.MarkOffline("timeout");
        vm.LongProcs.Should().BeEmpty();
    }

    [Fact]
    public void NotifierSendsOnePerProcess()
    {
        var path = Path.GetTempFileName();
        try
        {
            var nodes = new NodeStore(path);
            var settings = new ServerSettings(path) { NotificationsEnabled = true, NotifyWarn = true, NotifyDebounceSeconds = 0 };
            var notifier = new ThresholdNotifier(settings, nodes);
            var raised = new List<ThresholdNotifier.NotifyPayload>();
            notifier.Raised += (_, p) => raised.Add(p);

            var node = Node.LocalWindows("pc");
            var vm = new ServerViewModel(node);
            vm.ApplyLongProc(node, LongProcRule.Defaults);
            for (var m = 0; m <= 40; m++)
            {
                vm.Ingest(Make(T0.AddMinutes(m), LongProcTrackerTests.Orb(60)), T0.AddMinutes(m));
                notifier.EvaluateLongProcs(vm, T0.AddMinutes(m));
            }
            raised.Should().ContainSingle();
            raised[0].Metric.Should().Be(ThresholdNotifier.Metric.LongProc);
            raised[0].Severity.Should().Be(ThresholdNotifier.Severity.Warn);
            raised[0].Body.Should().Be("OrbStack Helper (pid 812) at 60% CPU for 30m. Started 11d ago.");

            // Off: no notification for a new process.
            vm.ApplyLongProc(node, LongProcRule.Defaults with { Notify = AlertNotify.Off });
            for (var m = 41; m <= 80; m++)
            {
                vm.Ingest(Make(T0.AddMinutes(m), LongProcTrackerTests.P(90, 9, "node", LongProcTrackerTests.Started)), T0.AddMinutes(m));
                notifier.EvaluateLongProcs(vm, T0.AddMinutes(m));
            }
            vm.LongProcs.Should().ContainSingle();
            raised.Should().ContainSingle();
        }
        finally { File.Delete(path); }
    }
}

public sealed class LongProcSettingsTests
{
    // Written by the Mac app (PersistedSettings with TOWERTAIL-107).
    private const string MacSettings = """
    {
      "schemaVersion": 1,
      "nodes": [
        {
          "id": "1e9e5d70-9aa3-4c8f-9ff4-7b7b9c5d8e2a",
          "displayName": "build-01",
          "kind": "ssh",
          "sshUser": "ci",
          "sshHost": "build-01",
          "tags": [],
          "enabled": true,
          "iconOnWarn": true,
          "iconOnCritical": true,
          "notifyOnWarn": true,
          "notifyOnCritical": true,
          "favorite": false,
          "customLongProc": {"enabled": true, "cpuPct": 90, "sustainSeconds": 7200, "notify": "all", "ignore": ["cc1plus"]},
          "ignoredProcs": ["OrbStack Helper"]
        },
        {
          "id": "2e9e5d70-9aa3-4c8f-9ff4-7b7b9c5d8e2a",
          "displayName": "mbp.local",
          "kind": "local",
          "tags": [],
          "enabled": true
        }
      ],
      "thresholds": {"cpuWarn": 0.75, "cpuCritical": 0.9, "memWarn": 0.75, "memCritical": 0.9, "diskWarn": 0.85, "diskCritical": 0.95},
      "longProc": {"enabled": true, "cpuPct": 80, "sustainSeconds": 3600, "notify": "off", "ignore": ["Xcode"]},
      "platform": {"darwin": {"launchAtLogin": true, "defaultTerminalApp": "iTerm"}}
    }
    """;

    [Fact]
    public void MacSettingsFileLoadsAndRoundTrips()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.WriteAllText(path, MacSettings);
            var p = SettingsPersistence.Load(path);
            p.LongProc.Should().Be(LongProcRule.Defaults with { CpuPct = 80, SustainSeconds = 3600, Notify = AlertNotify.Off, Ignore = new[] { "Xcode" } });
            p.Nodes[0].CustomLongProc.Should().Be(LongProcRule.Defaults with { CpuPct = 90, SustainSeconds = 7200, Ignore = new[] { "cc1plus" } });
            p.Nodes[0].IgnoredProcs.Should().Equal("OrbStack Helper");
            p.Nodes[1].CustomLongProc.Should().BeNull();
            p.Nodes[1].IgnoredProcs.Should().BeEmpty();

            var settings = new ServerSettings(path);
            settings.LongProc.Notify.Should().Be(AlertNotify.Off);
            settings.LongProc = settings.LongProc with { Ignore = new[] { "Xcode", "mds" } };
            settings.Persist();

            var root = JsonNode.Parse(File.ReadAllText(path))!.AsObject();
            root["longProc"]!["ignore"]!.AsArray().Select(n => n!.GetValue<string>()).Should().Equal("Xcode", "mds");
            root["nodes"]![0]!["customLongProc"]!["cpuPct"]!.GetValue<double>().Should().Be(90);
            root["nodes"]![0]!["ignoredProcs"]![0]!.GetValue<string>().Should().Be("OrbStack Helper");
            root["nodes"]![1]!.AsObject().ContainsKey("customLongProc").Should().BeFalse();
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void OldFileGetsTheDefaultRule()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.Copy(Path.Combine("TestData", "settings.mac.json"), path, overwrite: true);
            SettingsPersistence.Load(path).LongProc.Should().Be(LongProcRule.Defaults);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ExportAndImportCarryTheRule()
    {
        var src = Path.GetTempFileName();
        var dst = Path.GetTempFileName();
        try
        {
            File.WriteAllText(src, MacSettings);
            var export = SettingsTransfer.BuildFromCurrent(src);
            var json = SettingsTransfer.Encode(export);
            JsonNode.Parse(json)!["longProc"]!["cpuPct"]!.GetValue<double>().Should().Be(80);

            File.WriteAllText(dst, "{}");
            SettingsTransfer.Apply(SettingsTransfer.Decode(json), new SettingsImportOptions(false, true, false, true, true), dst);
            var p = SettingsPersistence.Load(dst);
            p.LongProc.CpuPct.Should().Be(80);
            p.Nodes[0].IgnoredProcs.Should().Equal("OrbStack Helper");
        }
        finally { File.Delete(src); File.Delete(dst); }
    }

    [Fact]
    public void ExportWithoutRuleKeepsTheCurrentOne()
    {
        var dst = Path.GetTempFileName();
        try
        {
            File.WriteAllText(dst, MacSettings);
            var export = SettingsTransfer.Decode("""{"globalThresholds": {"cpuWarn": 0.5}}""");
            export.LongProc.Should().BeNull();
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, false, false), dst);
            SettingsPersistence.Load(dst).LongProc.CpuPct.Should().Be(80);
        }
        finally { File.Delete(dst); }
    }

    [Fact]
    public void RemoteModelsOmitNullsAndMapBack()
    {
        var bare = JsonSerializer.Serialize(new RemoteNode(Guid.NewGuid(), "a", "ssh", "u", "h", null, true, false));
        var o = JsonNode.Parse(bare)!.AsObject();
        o.ContainsKey("customLongProc").Should().BeFalse();
        o.ContainsKey("ignoredProcs").Should().BeFalse();

        var full = new RemoteNode(Guid.NewGuid(), "a", "ssh", "u", "h", null, true, false,
            CustomLongProc: LongProcRule.Defaults with { CpuPct = 70 }, IgnoredProcs: new[] { "x" });
        var back = JsonSerializer.Deserialize<RemoteNode>(JsonSerializer.Serialize(full))!;
        back.CustomLongProc!.CpuPct.Should().Be(70);
        back.IgnoredProcs.Should().Equal("x");

        var rs = JsonSerializer.Deserialize<RemoteServerSettings>("""
            {"thresholds":{"cpuWarn":0.75,"cpuCritical":0.9,"memWarn":0.75,"memCritical":0.9,"diskWarn":0.85,"diskCritical":0.95},
             "localPollingIntervalSeconds":2,"sshPollingIntervalSeconds":10,"notificationsEnabled":true,"notifyWarn":true,
             "notifyCritical":true,"notifyDebounceSeconds":60,"autoUpdateSamplersEnabled":false,"postWakeGraceSeconds":15,
             "longProc":{"enabled":true,"cpuPct":50,"sustainSeconds":1800,"notify":"all","ignore":["mds"]}}
            """)!;
        rs.LongProc!.Ignore.Should().Equal("mds");
    }
}
