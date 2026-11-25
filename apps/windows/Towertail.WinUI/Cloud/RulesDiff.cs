using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

/// <summary>One local rule that differs from the rule in the cloud.</summary>
public sealed record RulesDiffRow(string Id, RulesScope Scope, string Rule, string CloudNow, string FromPc, RulesChange Change);

/// <summary>Where a rule applies: the org or one host.</summary>
public abstract record RulesScope
{
    public abstract string Title { get; }

    public sealed record Org : RulesScope { public override string Title => "Org"; }

    public sealed record Host(Guid NodeId, string Name) : RulesScope { public override string Title => Name; }
}

/// <summary>The value that a row moves to the cloud.</summary>
public abstract record RulesChange
{
    public sealed record OrgPair(ThresholdMetric Metric, ThresholdPair Pair) : RulesChange;
    /// <summary>All limits of the host health checks, as one rule.</summary>
    public sealed record OrgHealth(MetricThresholds Thresholds) : RulesChange;
    public sealed record OrgAlert(AlertMetric Metric, AlertRule Rule) : RulesChange;
    public sealed record OrgTolerance(double Tolerance) : RulesChange;
    public sealed record OrgMute(ThresholdMetric Metric, bool Muted) : RulesChange;
    public sealed record OrgLongProc(LongProcRule Rule) : RulesChange;
    public sealed record HostPair(Guid NodeId, ThresholdMetric Metric, ThresholdPair Pair) : RulesChange;
    public sealed record HostAlert(Guid NodeId, AlertMetric Metric, AlertRule Rule) : RulesChange;
    public sealed record HostMute(Guid NodeId, ThresholdMetric Metric, bool Muted) : RulesChange;
    public sealed record HostLongProc(Guid NodeId, LongProcRule Rule) : RulesChange;
    public sealed record HostIgnored(Guid NodeId, IReadOnlyList<string> Names) : RulesChange;
}

/// <summary>Compares the local rules with the cloud rules and builds the import. Mirrors the Mac <c>RulesDiff</c>.</summary>
public static class RulesDiff
{
    /// <summary>One row for each local rule that is not equal to the cloud rule. A host row exists only for a cloud host that the cloud knows.</summary>
    public static IReadOnlyList<RulesDiffRow> Rows(ServerSettings settings, IEnumerable<Node> nodes, RemoteRulesView cloud)
    {
        var rows = OrgRows(settings, cloud.Org);
        foreach (var node in nodes.Where(n => n.IsCloud))
            if (cloud.Hosts.FirstOrDefault(h => h.NodeId == node.Id) is { } host)
                rows.AddRange(HostRows(node, settings, cloud.Org, host.Host));
        return rows;
    }

    /// <summary>Starts from the cloud rules and applies each picked change. A rule that is not picked keeps the cloud value.</summary>
    public static RemoteRulesImport ImportBody(IEnumerable<RulesDiffRow> picked, RemoteRulesView cloud, string device)
    {
        var org = cloud.Org with { Origin = null };
        var orgChanged = false;
        List<Guid> order = [];
        Dictionary<Guid, RemoteHostRules> hosts = [];

        void Edit(Guid id, Func<RemoteHostRules, RemoteHostRules> apply)
        {
            if (!hosts.TryGetValue(id, out var current))
            {
                order.Add(id);
                current = (cloud.Hosts.FirstOrDefault(h => h.NodeId == id)?.Host ?? new RemoteHostRules()) with { Origin = null };
            }
            hosts[id] = apply(current);
        }

        foreach (var row in picked)
        {
            switch (row.Change)
            {
                case RulesChange.OrgPair c: org = org with { Thresholds = org.Thresholds.With(c.Metric, c.Pair) }; orgChanged = true; break;
                case RulesChange.OrgHealth c:
                    var t = org.Thresholds;
                    foreach (var m in ThresholdMetrics.Health) t = t.With(m, c.Thresholds.Get(m));
                    t = t with { Health = t.HealthOrDefault with { MacMemPressure = c.Thresholds.HealthOrDefault.MacMemPressure } };
                    org = org with { Thresholds = t };
                    orgChanged = true;
                    break;
                case RulesChange.OrgAlert c: org = org with { Alerts = org.Alerts.With(c.Metric, c.Rule) }; orgChanged = true; break;
                case RulesChange.OrgTolerance c: org = org with { Alerts = org.Alerts with { Tolerance = c.Tolerance } }; orgChanged = true; break;
                case RulesChange.OrgMute c: org = org with { Muted = org.Muted.With(c.Metric.Signal(), c.Muted) }; orgChanged = true; break;
                case RulesChange.OrgLongProc c: org = org with { LongProc = c.Rule }; orgChanged = true; break;
                case RulesChange.HostPair c:
                    Edit(c.NodeId, h => h with { Thresholds = (h.Thresholds ?? new ThresholdOverrides()).With(c.Metric, c.Pair) });
                    break;
                case RulesChange.HostAlert c:
                    Edit(c.NodeId, h => h with { Alerts = (h.Alerts ?? new RemoteAlertOverrides()).With(c.Metric, c.Rule) });
                    break;
                case RulesChange.HostMute c: Edit(c.NodeId, h => h with { Muted = h.Muted.With(c.Metric.Signal(), c.Muted) }); break;
                case RulesChange.HostLongProc c: Edit(c.NodeId, h => h with { LongProc = c.Rule }); break;
                case RulesChange.HostIgnored c: Edit(c.NodeId, h => h with { IgnoredProcs = c.Names }); break;
            }
        }

        return new RemoteRulesImport(
            orgChanged ? org : null,
            order.Select(id => new RemoteRulesImportHost(id, hosts[id])).ToList(),
            "windows", device);
    }

    private static List<RulesDiffRow> OrgRows(ServerSettings settings, RemoteOrgRules cloud)
    {
        List<RulesDiffRow> rows = [];
        var scope = new RulesScope.Org();
        var local = settings.Thresholds;
        foreach (var m in ThresholdMetrics.Usage.Where(m => local.Get(m) != cloud.Thresholds.Get(m)))
            rows.Add(new($"org.{m.Key()}.pair", scope, $"{m.Title()} warn / critical",
                AlertText.Pair(m, cloud.Thresholds.Get(m)), AlertText.Pair(m, local.Get(m)),
                new RulesChange.OrgPair(m, local.Get(m))));

        var alerts = settings.Alerts;
        foreach (var m in AlertMetrics.All.Where(m => alerts.For(m) != cloud.Alerts.For(m)))
        {
            var withNotify = alerts.For(m).Notify != cloud.Alerts.For(m).Notify;
            rows.Add(new($"org.{AlertKey(m)}.alert", scope, $"{AlertTitle(m)} alert after",
                AlertLine(cloud.Alerts.For(m), withNotify), AlertLine(alerts.For(m), withNotify),
                new RulesChange.OrgAlert(m, alerts.For(m))));
        }

        if (HealthLimitCount(local, cloud.Thresholds) > 0)
            rows.Add(new("org.health.limits", scope, "Health limits",
                LimitsText(HealthLimitCount(cloud.Thresholds, MetricThresholds.Defaults)),
                LimitsText(HealthLimitCount(local, MetricThresholds.Defaults)),
                new RulesChange.OrgHealth(local)));

        if (alerts.Tolerance != cloud.Alerts.Tolerance)
            rows.Add(new("org.tolerance", scope, "Over the line",
                AlertText.Percent(cloud.Alerts.Tolerance), AlertText.Percent(alerts.Tolerance),
                new RulesChange.OrgTolerance(alerts.Tolerance)));

        foreach (var m in ThresholdMetrics.All.Where(m => settings.MutedSignals.Contains(m.Signal()) != cloud.Muted.Contains(m.Signal())))
        {
            var on = settings.MutedSignals.Contains(m.Signal());
            rows.Add(new($"org.{m.Key()}.mute", scope, $"{m.Title()} mute",
                MuteText(cloud.Muted.Contains(m.Signal())), MuteText(on), new RulesChange.OrgMute(m, on)));
        }

        if (settings.LongProc != cloud.LongProc)
            rows.Add(new("org.longproc", scope, "Long-running rule",
                AlertText.LongProc(cloud.LongProc), AlertText.LongProc(settings.LongProc),
                new RulesChange.OrgLongProc(settings.LongProc)));
        return rows;
    }

    private static List<RulesDiffRow> HostRows(Node node, ServerSettings settings, RemoteOrgRules org, RemoteHostRules host)
    {
        List<RulesDiffRow> rows = [];
        var scope = new RulesScope.Host(node.Id, node.DisplayName);
        var key = node.Id.ToString().ToUpperInvariant();

        if (node.ThresholdOverrides is { } local)
            foreach (var m in ThresholdMetrics.All)
            {
                if (local.Get(m) is not { } pair || pair == host.Thresholds?.Get(m)) continue;
                var now = host.Thresholds?.Get(m) is { } cloudPair
                    ? AlertText.Pair(m, cloudPair)
                    : $"org ({AlertText.Pair(m, org.Thresholds.Get(m))})";
                rows.Add(new($"host.{key}.{m.Key()}.pair", scope, $"{m.Title()} warn / critical",
                    now, AlertText.Pair(m, pair), new RulesChange.HostPair(node.Id, m, pair)));
            }

        if (node.CustomAlerts is { } custom)
        {
            var changed = RemoteAlertOverrides.Diff(custom, settings.Alerts);
            foreach (var m in AlertMetrics.All)
            {
                if (changed.Rule(m) is not { } rule || rule == host.Alerts?.Rule(m)) continue;
                var cloudRule = host.Alerts?.Rule(m) ?? org.Alerts.For(m);
                var withNotify = cloudRule.Notify != rule.Notify;
                var now = AlertLine(cloudRule, withNotify);
                rows.Add(new($"host.{key}.{AlertKey(m)}.alert", scope, $"{AlertTitle(m)} alert after",
                    host.Alerts?.Rule(m) is null ? $"org ({now})" : now,
                    AlertLine(rule, withNotify), new RulesChange.HostAlert(node.Id, m, rule)));
            }
        }

        foreach (var m in ThresholdMetrics.All.Where(m => node.MutedSignals.Contains(m.Signal()) && !host.Muted.Contains(m.Signal())))
            rows.Add(new($"host.{key}.{m.Key()}.mute", scope, $"{m.Title()} mute",
                MuteText(false), MuteText(true), new RulesChange.HostMute(node.Id, m, true)));

        if (node.CustomLongProc is { } longRule && longRule != host.LongProc)
            rows.Add(new($"host.{key}.longproc", scope, "Long-running rule",
                host.LongProc is { } cloudRule2 ? AlertText.LongProc(cloudRule2) : "org",
                AlertText.LongProc(longRule), new RulesChange.HostLongProc(node.Id, longRule)));

        if (node.IgnoredProcs.Count > 0 && !node.IgnoredProcs.ToHashSet().SetEquals(host.IgnoredProcs))
            rows.Add(new($"host.{key}.ignored", scope, "Ignored processes",
                host.IgnoredProcs.Count == 0 ? "—" : string.Join(", ", host.IgnoredProcs),
                string.Join(", ", node.IgnoredProcs), new RulesChange.HostIgnored(node.Id, node.IgnoredProcs)));
        return rows;
    }

    private static string AlertKey(AlertMetric m) => m.ToString().ToLowerInvariant();

    private static string AlertTitle(AlertMetric m)
        => m == AlertMetric.Health ? "Health checks" : Enum.Parse<ThresholdMetric>(m.ToString()).Title();

    private static string AlertLine(AlertRule r, bool withNotify)
    {
        var time = AlertText.Sustain(r.SustainSeconds).ToLowerInvariant();
        return withNotify ? $"{time} · {AlertText.Notify(r.Notify).ToLowerInvariant()}" : time;
    }

    private static string MuteText(bool muted) => muted ? "muted" : "not muted";

    private static string LimitsText(int n) => n == 0 ? "none" : $"{n} {(n == 1 ? "limit" : "limits")}";

    /// <summary>The health limits of <paramref name="t"/> that differ from <paramref name="other"/>.</summary>
    private static int HealthLimitCount(MetricThresholds t, MetricThresholds other)
        => ThresholdMetrics.Health.Count(m => t.Get(m) != other.Get(m))
           + (t.HealthOrDefault.MacMemPressure != other.HealthOrDefault.MacMemPressure ? 1 : 0);
}
