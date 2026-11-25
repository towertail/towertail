using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class RulesDiffTests : IDisposable
{
    private readonly string _path = Path.Combine(Path.GetTempPath(), $"tt-diff-{Guid.NewGuid():N}.json");
    private readonly ServerSettings _settings;

    public RulesDiffTests() => _settings = new ServerSettings(_path);

    public void Dispose()
    {
        if (File.Exists(_path)) File.Delete(_path);
    }

    private static Node CloudHost(string name = "web")
        => new() { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = name, Cloud = new CloudLink(DateTime.UtcNow) };

    /// <summary>Cloud rules that equal the local rules.</summary>
    private RemoteRulesView Matching(params Node[] hosts) => new(
        new RemoteOrgRules
        {
            Thresholds = _settings.Thresholds, Alerts = _settings.Alerts,
            Muted = _settings.MutedSignals, LongProc = _settings.LongProc,
        },
        hosts.Select(h => new RemoteRulesHost(h.Id, new RemoteHostRules(), new RemoteEffectiveRules())).ToList());

    private static ThresholdPair Pair(double warn, double critical) => new(warn, critical);

    [Fact]
    public void OrgPairDiff()
    {
        _settings.Thresholds = _settings.Thresholds with { CpuWarn = 0.75, CpuCritical = 0.9 };
        var cloud = Matching();
        cloud = cloud with { Org = cloud.Org with { Thresholds = cloud.Org.Thresholds with { CpuWarn = 0.8, CpuCritical = 0.9 } } };

        var rows = RulesDiff.Rows(_settings, [], cloud);

        var row = rows.Should().ContainSingle().Subject;
        row.Scope.Should().Be(new RulesScope.Org());
        row.Rule.Should().Be("CPU warn / critical");
        row.CloudNow.Should().Be("80 / 90");
        row.FromPc.Should().Be("75 / 90");
        row.Change.Should().Be(new RulesChange.OrgPair(ThresholdMetric.Cpu, Pair(0.75, 0.9)));
    }

    [Fact]
    public void EqualRulesGiveNoRow()
    {
        var host = CloudHost();
        RulesDiff.Rows(_settings, [host], Matching(host)).Should().BeEmpty();
    }

    [Fact]
    public void OrgAlertAndMuteAndLongProcRows()
    {
        var cloud = Matching();
        cloud = cloud with
        {
            Org = cloud.Org with
            {
                Alerts = cloud.Org.Alerts.With(AlertMetric.Cpu, new AlertRule(0, _settings.Alerts.Cpu.Notify)),
                Muted = cloud.Org.Muted.With(AlertSignal.Disk, true),
                LongProc = cloud.Org.LongProc with { CpuPct = 90 },
            },
        };

        var rows = RulesDiff.Rows(_settings, [], cloud);

        rows.Select(r => r.Rule).Should().Equal("CPU alert after", "Disk mute", "Long-running rule");
        rows[0].CloudNow.Should().Be("at once");
        rows[0].FromPc.Should().Be("5 min");
        rows[1].FromPc.Should().Be("not muted");
        rows[1].Change.Should().Be(new RulesChange.OrgMute(ThresholdMetric.Disk, false));
    }

    [Fact]
    public void HostOverrideAgainstOrg()
    {
        var node = CloudHost("sling-master") with { ThresholdOverrides = new ThresholdOverrides(Mem: Pair(0.85, 0.95)) };
        _settings.Thresholds = _settings.Thresholds with { MemWarn = 0.8, MemCritical = 0.9 };
        var cloud = Matching(node);

        var rows = RulesDiff.Rows(_settings, [node], cloud);

        var row = rows.Should().ContainSingle().Subject;
        row.Scope.Should().Be(new RulesScope.Host(node.Id, "sling-master"));
        row.Rule.Should().Be("Memory warn / critical");
        row.CloudNow.Should().Be("org (80 / 90)");
        row.FromPc.Should().Be("85 / 95");
    }

    [Fact]
    public void EqualHostOverrideGivesNoRow()
    {
        var node = CloudHost() with { ThresholdOverrides = new ThresholdOverrides(Mem: Pair(0.85, 0.95)) };
        var cloud = Matching(node);
        cloud = cloud with
        {
            Hosts = [cloud.Hosts[0] with { Host = new RemoteHostRules { Thresholds = new ThresholdOverrides(Mem: Pair(0.85, 0.95)) } }],
        };

        RulesDiff.Rows(_settings, [node], cloud).Should().BeEmpty();
    }

    [Fact]
    public void SkipsLocalAndSshHosts()
    {
        var overrides = new ThresholdOverrides(Cpu: Pair(0.5, 0.6));
        var ssh = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "db", ThresholdOverrides = overrides };
        var local = Node.LocalWindows() with { ThresholdOverrides = overrides };
        var unknown = CloudHost("ghost") with { ThresholdOverrides = overrides };

        // A host that the cloud does not know has no row.
        RulesDiff.Rows(_settings, [ssh, local, unknown], Matching()).Should().BeEmpty();
    }

    [Fact]
    public void CustomAlertsSendsOnlyChangedRules()
    {
        var alerts = _settings.Alerts.With(AlertMetric.Cpu, new AlertRule(60, _settings.Alerts.Cpu.Notify));
        var node = CloudHost() with { CustomAlerts = alerts };

        var rows = RulesDiff.Rows(_settings, [node], Matching(node));

        // The memory, disk and health rules equal the global rules.
        var row = rows.Should().ContainSingle().Subject;
        row.Change.Should().Be(new RulesChange.HostAlert(node.Id, AlertMetric.Cpu, new AlertRule(60, alerts.Cpu.Notify)));
        row.CloudNow.Should().Be("org (5 min)");
        row.FromPc.Should().Be("1 min");
    }

    [Fact]
    public void HostMuteLongProcAndIgnoredRows()
    {
        var node = CloudHost() with
        {
            MutedSignals = new MutedSignals([AlertSignal.Cpu]),
            CustomLongProc = new LongProcRule { Enabled = true, CpuPct = 60, SustainSeconds = 600, Notify = AlertNotify.All },
            IgnoredProcs = ["pg_dump"],
        };

        var rows = RulesDiff.Rows(_settings, [node], Matching(node));

        rows.Select(r => r.Rule).Should().Equal("CPU mute", "Long-running rule", "Ignored processes");
        rows[1].CloudNow.Should().Be("org");
        rows[1].FromPc.Should().Be("60% · 10 min");
        rows[2].CloudNow.Should().Be("—");
        rows[2].FromPc.Should().Be("pg_dump");
    }

    [Fact]
    public void HealthLimitsRow()
    {
        _settings.Thresholds = _settings.Thresholds.With(ThresholdMetric.Files, Pair(0.5, 0.6))
            .WithHealth(5000, 20000, 10, 2000);
        var cloud = Matching();
        cloud = cloud with { Org = cloud.Org with { Thresholds = MetricThresholds.Defaults } };

        var rows = RulesDiff.Rows(_settings, [], cloud);

        var row = rows.Should().ContainSingle().Subject;
        row.Rule.Should().Be("Health limits");
        row.CloudNow.Should().Be("none");
        row.FromPc.Should().Be("2 limits");
    }

    [Fact]
    public void ImportBodyKeepsUnpickedCloudValues()
    {
        var node = CloudHost() with
        {
            ThresholdOverrides = new ThresholdOverrides(Mem: Pair(0.85, 0.95)),
            IgnoredProcs = ["pg_dump"],
        };
        _settings.Thresholds = _settings.Thresholds with { CpuWarn = 0.75, CpuCritical = 0.9 };
        var cloud = Matching(node);
        cloud = cloud with
        {
            Org = cloud.Org with { Thresholds = cloud.Org.Thresholds with { CpuWarn = 0.8, CpuCritical = 0.9 } },
            Hosts = [cloud.Hosts[0] with
            {
                Host = new RemoteHostRules { IgnoredProcs = ["old"], Thresholds = new ThresholdOverrides(Disk: Pair(0.7, 0.8)) },
            }],
        };
        var rows = RulesDiff.Rows(_settings, [node], cloud);
        rows.Should().HaveCount(3);

        var onlyMem = rows.Where(r => r.Rule == "Memory warn / critical").ToList();
        var body = RulesDiff.ImportBody(onlyMem, cloud, "test-pc");

        body.Org.Should().BeNull("no org row is picked");
        var rules = body.Hosts.Should().ContainSingle().Subject.Rules;
        rules.Thresholds!.Mem.Should().Be(Pair(0.85, 0.95));
        rules.Thresholds.Disk.Should().Be(Pair(0.7, 0.8), "the cloud value stays");
        rules.IgnoredProcs.Should().Equal("old");
        body.Client.Should().Be("windows");
        body.Device.Should().Be("test-pc");

        var all = RulesDiff.ImportBody(rows, cloud, "test-pc");
        all.Org!.Thresholds.CpuWarn.Should().Be(0.75);
        all.Hosts[0].Rules.IgnoredProcs.Should().Equal("pg_dump");
        all.Org.Origin.Should().BeNull();
    }

    [Fact]
    public void FixtureDecodesAndMatchesItself()
    {
        var json = File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "TestData", "rules-view.json"));
        var view = System.Text.Json.JsonSerializer.Deserialize<RemoteRulesView>(json, RemoteClient.WireJson)!;

        view.Hosts.Should().HaveCount(2);
        view.Hosts[0].Effective.Muted.Contains(AlertSignal.Long).Should().BeTrue();
        view.Hosts[0].Effective.Ignore.Should().BeEmpty("a null ignore reads as an empty list");
    }
}
