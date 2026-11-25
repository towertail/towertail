import Foundation

/// One local rule that differs from the rule in the cloud.
struct RulesDiffRow: Identifiable, Equatable {
    enum Scope: Equatable {
        case org
        case host(UUID, String)

        var title: String {
            switch self {
            case .org: return "Org"
            case .host(_, let name): return name
            }
        }
    }

    enum Change: Equatable {
        case orgPair(ThresholdMetric, ThresholdPair)
        /// All limits of the host health checks, as one rule.
        case orgHealth(MetricThresholds)
        case orgAlert(Metric, AlertRule)
        case orgTolerance(Double)
        case orgMute(ThresholdMetric, Bool)
        case orgLongProc(LongProcRule)
        case hostPair(UUID, ThresholdMetric, ThresholdPair)
        case hostAlert(UUID, Metric, AlertRule)
        case hostMute(UUID, ThresholdMetric, Bool)
        case hostLongProc(UUID, LongProcRule)
        case hostIgnored(UUID, [String])
    }

    let id: String
    let scope: Scope
    let rule: String
    let cloudNow: String
    let fromMac: String
    let change: Change
}

@MainActor
enum RulesDiff {
    /// One row for each local rule that is not equal to the cloud rule.
    /// A host row exists only for a Cloud host that the cloud knows.
    static func rows(settings: ServerSettings, nodes: [Node], cloud: RemoteRulesView) -> [RulesDiffRow] {
        var out = orgRows(settings: settings, cloud: cloud.org)
        for node in nodes where node.isCloud {
            guard let host = cloud.hosts.first(where: { $0.nodeId == node.id }) else { continue }
            out += hostRows(node: node, settings: settings, org: cloud.org, host: host.host)
        }
        return out
    }

    /// Starts from the cloud rules and applies each picked change. A rule
    /// that is not picked keeps the cloud value.
    static func importBody(_ picked: [RulesDiffRow], cloud: RemoteRulesView, device: String) -> RemoteRulesImport {
        var org = cloud.org
        org.origin = nil
        var orgChanged = false
        var order: [UUID] = []
        var hosts: [UUID: RemoteHostRules] = [:]

        func edit(_ id: UUID, _ apply: (inout RemoteHostRules) -> Void) {
            if hosts[id] == nil {
                order.append(id)
                var base = cloud.hosts.first { $0.nodeId == id }?.host ?? RemoteHostRules()
                base.origin = nil
                hosts[id] = base
            }
            apply(&hosts[id]!)
        }

        for row in picked {
            switch row.change {
            case .orgPair(let m, let p): org.thresholds[m] = p; orgChanged = true
            case .orgHealth(let t):
                for m in ThresholdMetric.health { org.thresholds[m] = t[m] }
                org.thresholds.health.macMemPressure = t.health.macMemPressure
                orgChanged = true
            case .orgAlert(let m, let r): org.alerts[m] = r; orgChanged = true
            case .orgTolerance(let t):
                let a = org.alerts
                org.alerts = AlertRules(cpu: a.cpu, mem: a.mem, disk: a.disk, health: a.health, tolerance: t)
                orgChanged = true
            case .orgMute(let m, let on): org.muted.set(m, muted: on); orgChanged = true
            case .orgLongProc(let r): org.longProc = r; orgChanged = true
            case .hostPair(let id, let m, let p):
                edit(id) { var t = $0.thresholds ?? ThresholdOverrides(); t[m] = p; $0.thresholds = t }
            case .hostAlert(let id, let m, let r):
                edit(id) { var a = $0.alerts ?? RemoteAlertOverrides(); a.set(m, r); $0.alerts = a }
            case .hostMute(let id, let m, let on): edit(id) { $0.muted.set(m, muted: on) }
            case .hostLongProc(let id, let r): edit(id) { $0.longProc = r }
            case .hostIgnored(let id, let names): edit(id) { $0.ignoredProcs = names }
            }
        }

        return RemoteRulesImport(
            org: orgChanged ? org : nil,
            hosts: order.compactMap { id in
                hosts[id].map { h in
                    RemoteRulesImport.HostPart(nodeId: id, rules: RemoteHostRules(
                        thresholds: h.thresholds, alerts: h.alerts, muted: h.muted,
                        longProc: h.longProc, ignoredProcs: h.ignoredProcs))
                }
            },
            client: "mac",
            device: device
        )
    }

    // MARK: - Org

    private static func orgRows(settings: ServerSettings, cloud: RemoteOrgRules) -> [RulesDiffRow] {
        var out: [RulesDiffRow] = []
        let local = settings.thresholds
        for m in ThresholdMetric.usage where local[m] != cloud.thresholds[m] {
            out.append(RulesDiffRow(
                id: "org.\(m.rawValue).pair", scope: .org, rule: "\(m.title) warn / critical",
                cloudNow: pairText(m, cloud.thresholds[m]), fromMac: pairText(m, local[m]),
                change: .orgPair(m, local[m])))
        }
        let alerts = settings.alertRules
        for m in [Metric.cpu, .mem, .disk, .health] where alerts[m] != cloud.alerts[m] {
            let withNotify = alerts[m].notify != cloud.alerts[m].notify
            out.append(RulesDiffRow(
                id: "org.\(m.rawValue).alert", scope: .org, rule: "\(alertTitle(m)) alert after",
                cloudNow: alertText(cloud.alerts[m], withNotify: withNotify),
                fromMac: alertText(alerts[m], withNotify: withNotify),
                change: .orgAlert(m, alerts[m])))
        }
        let healthChanged = healthLimitCount(local, against: cloud.thresholds)
        if healthChanged > 0 {
            out.append(RulesDiffRow(
                id: "org.health.limits", scope: .org, rule: "Health limits",
                cloudNow: limitsText(healthLimitCount(cloud.thresholds, against: .defaults)),
                fromMac: limitsText(healthLimitCount(local, against: .defaults)),
                change: .orgHealth(local)))
        }
        if alerts.tolerance != cloud.alerts.tolerance {
            out.append(RulesDiffRow(
                id: "org.tolerance", scope: .org, rule: "Over the line",
                cloudNow: percent(cloud.alerts.tolerance), fromMac: percent(alerts.tolerance),
                change: .orgTolerance(alerts.tolerance)))
        }
        for m in ThresholdMetric.allCases where settings.mutedSignals.contains(m) != cloud.muted.contains(m) {
            let on = settings.mutedSignals.contains(m)
            out.append(RulesDiffRow(
                id: "org.\(m.rawValue).mute", scope: .org, rule: "\(m.title) mute",
                cloudNow: cloud.muted.contains(m) ? "muted" : "not muted", fromMac: on ? "muted" : "not muted",
                change: .orgMute(m, on)))
        }
        if settings.longProc != cloud.longProc {
            out.append(RulesDiffRow(
                id: "org.longproc", scope: .org, rule: "Long-running rule",
                cloudNow: longProcText(cloud.longProc), fromMac: longProcText(settings.longProc),
                change: .orgLongProc(settings.longProc)))
        }
        return out
    }

    // MARK: - Hosts

    private static func hostRows(node: Node, settings: ServerSettings, org: RemoteOrgRules, host: RemoteHostRules) -> [RulesDiffRow] {
        var out: [RulesDiffRow] = []
        let scope = RulesDiffRow.Scope.host(node.id, node.displayName)
        let key = node.id.uuidString

        if let local = node.thresholdOverrides {
            for m in ThresholdMetric.allCases {
                guard let pair = local[m], pair != host.thresholds?[m] else { continue }
                let now = host.thresholds?[m].map { pairText(m, $0) } ?? "org (\(pairText(m, org.thresholds[m])))"
                out.append(RulesDiffRow(
                    id: "host.\(key).\(m.rawValue).pair", scope: scope, rule: "\(m.title) warn / critical",
                    cloudNow: now, fromMac: pairText(m, pair), change: .hostPair(node.id, m, pair)))
            }
        }
        if let custom = node.customAlerts {
            let changed = RemoteAlertOverrides.diff(custom, from: settings.alertRules)
            for m in [Metric.cpu, .mem, .disk, .health] {
                guard let rule = changed.rule(m), rule != host.alerts?.rule(m) else { continue }
                let cloudRule = host.alerts?.rule(m) ?? org.alerts[m]
                let withNotify = cloudRule.notify != rule.notify
                let now = alertText(cloudRule, withNotify: withNotify)
                out.append(RulesDiffRow(
                    id: "host.\(key).\(m.rawValue).alert", scope: scope, rule: "\(alertTitle(m)) alert after",
                    cloudNow: host.alerts?.rule(m) == nil ? "org (\(now))" : now,
                    fromMac: alertText(rule, withNotify: withNotify), change: .hostAlert(node.id, m, rule)))
            }
        }
        for m in ThresholdMetric.allCases where node.mutedSignals.contains(m) && !host.muted.contains(m) {
            out.append(RulesDiffRow(
                id: "host.\(key).\(m.rawValue).mute", scope: scope, rule: "\(m.title) mute",
                cloudNow: "not muted", fromMac: "muted", change: .hostMute(node.id, m, true)))
        }
        if let rule = node.customLongProc, rule != host.longProc {
            out.append(RulesDiffRow(
                id: "host.\(key).longproc", scope: scope, rule: "Long-running rule",
                cloudNow: host.longProc.map(longProcText) ?? "org", fromMac: longProcText(rule),
                change: .hostLongProc(node.id, rule)))
        }
        if !node.ignoredProcs.isEmpty, Set(node.ignoredProcs) != Set(host.ignoredProcs) {
            out.append(RulesDiffRow(
                id: "host.\(key).ignored", scope: scope, rule: "Ignored processes",
                cloudNow: host.ignoredProcs.isEmpty ? "—" : host.ignoredProcs.joined(separator: ", "),
                fromMac: node.ignoredProcs.joined(separator: ", "), change: .hostIgnored(node.id, node.ignoredProcs)))
        }
        return out
    }

    // MARK: - Text

    private static func number(_ m: ThresholdMetric, _ v: Double) -> String {
        let n = Int((v * m.scale).rounded())
        return m.isCount ? n.formatted(.number.grouping(.automatic)) : "\(n)"
    }

    private static func pairText(_ m: ThresholdMetric, _ p: ThresholdPair) -> String {
        "\(number(m, p.warn)) / \(number(m, p.critical))"
    }

    private static func percent(_ v: Double) -> String { "\(Int((v * 100).rounded()))%" }

    private static func alertTitle(_ m: Metric) -> String {
        m == .health ? "Health checks" : ThresholdMetric(m)?.title ?? m.rawValue
    }

    private static func alertText(_ r: AlertRule, withNotify: Bool) -> String {
        let time = SustainPicker.label(r.sustainSeconds).lowercased()
        return withNotify ? "\(time) · \(CloudHostRulesForm.notifyText(r.notify).lowercased())" : time
    }

    private static func longProcText(_ r: LongProcRule) -> String {
        r.enabled ? "\(Int(r.cpuPct))% · \(SustainPicker.label(r.sustainSeconds))" : "off"
    }

    private static func limitsText(_ n: Int) -> String {
        n == 0 ? "none" : "\(n) \(n == 1 ? "limit" : "limits")"
    }

    /// The health limits of `t` that differ from `other`.
    private static func healthLimitCount(_ t: MetricThresholds, against other: MetricThresholds) -> Int {
        ThresholdMetric.health.filter { t[$0] != other[$0] }.count
            + (t.health.macMemPressure != other.health.macMemPressure ? 1 : 0)
    }
}
