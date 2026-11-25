import SwiftUI

/// The rules of a Cloud host. The Cloud holds them. Edits go to the Cloud
/// at once. Use it inside a `Form`.
struct CloudHostRulesView: View {
    let nodeID: UUID

    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(ServerStore.self) private var serverStore

    var body: some View {
        if let rules = cloud?.rules {
            CloudHostRulesForm(
                rules: rules,
                nodeID: nodeID,
                me: cloud?.account.me,
                hostOS: serverStore.serverVMs.first { $0.id == nodeID }?.hostOS
            )
        } else {
            Section("Rules from the cloud") {
                Text("Sign in to Cloud to see the rules of this host.").foregroundStyle(.secondary)
            }
        }
    }
}

struct CloudHostRulesForm: View {
    let rules: CloudRules
    let nodeID: UUID
    let me: CloudMe?
    let hostOS: String?

    @State private var draft: RemoteHostRules?
    @State private var inflight = 0
    @State private var chain: Task<Void, Never>?
    @State private var problem: String?

    private var canEdit: Bool { me?.isAdmin == true }
    private var canMute: Bool { me?.canMute == true }
    private var host: RemoteHostRules { draft ?? rules.host(nodeID) ?? RemoteHostRules() }
    private var org: RemoteOrgRules { rules.org ?? RemoteOrgRules() }
    private var effective: RemoteEffectiveRules { rules.effective(for: nodeID) ?? RemoteEffectiveRules() }

    var body: some View {
        Group {
            Section {
                Text(originLine).foregroundStyle(.secondary)
                if !canEdit {
                    Text("Only an org admin can change the rules of a Cloud host.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if let problem {
                    Text(problem).font(.caption).foregroundStyle(ThresholdTint.critical.color)
                }
            } header: {
                Text("Rules from the cloud")
            }

            Section {
                ForEach(ThresholdMetric.usage) { pairRow($0) }
                timingRow("CPU", .cpu)
                timingRow("Memory", .mem)
                timingRow("Disk", .disk)
                toleranceRow
            } header: {
                Text("Thresholds and alert timing")
            } footer: {
                Text("A metric uses the org value until you set a value for this host.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            Section {
                ForEach(ThresholdMetric.health) { pairRow($0) }
                timingRow("Health checks", .health)
            } header: {
                Text("Host health")
            } footer: {
                Text("A muted signal sends no notification and does not tint the menu-bar icon.")
                    .font(.caption).foregroundStyle(.secondary)
            }

            longProcSection
        }
        .onAppear { draft = rules.host(nodeID) }
        .onChange(of: rules.host(nodeID)) { _, fresh in
            if inflight == 0 { draft = fresh }
        }
    }

    // MARK: rows

    private var originLine: String {
        guard let origin = host.origin ?? org.origin else { return "Set on the web." }
        let from = origin.client == "web" ? "the web" : origin.source
        guard let at = origin.at else { return "Set from \(from)." }
        return "Set from \(from) on \(at.formatted(date: .abbreviated, time: .omitted))."
    }

    private func sourceButton(isHost: Bool, set: @escaping (Bool) -> Void) -> some View {
        Button(isHost ? "This host" : "Org") { set(!isHost) }
            .controlSize(.small)
            .disabled(!canEdit)
            .help(isHost ? "Use the org value" : "Set a value for this host")
            .frame(width: 72, alignment: .leading)
    }

    private func pairRow(_ m: ThresholdMetric) -> some View {
        let orgPair = org.thresholds.pair(for: m, hostOS: hostOS)
        let own = host.thresholds?[m]
        let inherited = org.muted.contains(m)
        let muted = inherited || host.muted.contains(m)
        return HStack(spacing: ThresholdColumns.spacing) {
            Text(m.title)
                .foregroundStyle(muted ? .secondary : .primary)
                .frame(width: ThresholdColumns.name, alignment: .leading)
                .help(m.help(hostOS: hostOS) ?? "")
            sourceButton(isHost: own != nil) { toHost in
                edit { $0.thresholds = Self.with($0.thresholds, m, toHost ? orgPair : nil) }
            }
            if let own {
                ThresholdPairFields(
                    pair: Binding(get: { host.thresholds?[m] ?? own }, set: { new in
                        edit { $0.thresholds = Self.with($0.thresholds, m, new) }
                    }),
                    scale: m.scale, unit: m.unit, range: m.range
                )
                .disabled(!canEdit)
            } else {
                Text("\(m.format(orgPair.warn)) / \(m.format(orgPair.critical))").foregroundStyle(.secondary)
            }
            Spacer()
            if muted { Text("muted").font(.caption).foregroundStyle(ThresholdTint.warn.color) }
            muteToggle(muted: host.muted.contains(m), inherited: inherited) { on in
                Task { await mute { try await rules.setMute(m, nodeID: nodeID, muted: on) } }
            }
        }
    }

    private func timingRow(_ title: String, _ metric: Metric) -> some View {
        let orgRule = org.alerts[metric]
        let own = host.alerts?.rule(metric)
        return HStack(spacing: ThresholdColumns.spacing) {
            Text("\(title) alert after")
                .frame(width: ThresholdColumns.name, alignment: .leading)
            sourceButton(isHost: own != nil) { toHost in
                edit { $0.alerts = Self.with($0.alerts, metric, toHost ? orgRule : nil) }
            }
            if let own {
                SustainPicker(seconds: Binding(get: { host.alerts?.rule(metric)?.sustainSeconds ?? own.sustainSeconds }, set: { s in
                    edit { h in
                        let notify = h.alerts?.rule(metric)?.notify ?? own.notify
                        h.alerts = Self.with(h.alerts, metric, AlertRule(sustainSeconds: s, notify: notify))
                    }
                }))
                .labelsHidden()
                .frame(width: ThresholdColumns.sustain)
                .disabled(!canEdit)
                NotifyPicker(notify: Binding(get: { host.alerts?.rule(metric)?.notify ?? own.notify }, set: { n in
                    edit { h in
                        let s = h.alerts?.rule(metric)?.sustainSeconds ?? own.sustainSeconds
                        h.alerts = Self.with(h.alerts, metric, AlertRule(sustainSeconds: s, notify: n))
                    }
                }))
                .labelsHidden()
                .frame(width: ThresholdColumns.notify)
                .disabled(!canEdit)
            } else {
                Text("\(SustainPicker.label(orgRule.sustainSeconds)) · \(Self.notifyText(orgRule.notify))")
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    private var toleranceRow: some View {
        let own = host.alerts?.tolerance
        return HStack(spacing: ThresholdColumns.spacing) {
            Text("Over the line")
                .frame(width: ThresholdColumns.name, alignment: .leading)
            sourceButton(isHost: own != nil) { toHost in
                edit { h in
                    var a = h.alerts ?? RemoteAlertOverrides()
                    a.tolerance = toHost ? org.alerts.tolerance : nil
                    h.alerts = a
                }
            }
            if let own {
                AlertToleranceControl(tolerance: Binding(get: { host.alerts?.tolerance ?? own }, set: { t in
                    edit { h in
                        var a = h.alerts ?? RemoteAlertOverrides()
                        a.tolerance = t
                        h.alerts = a
                    }
                }))
                .labelsHidden()
                .disabled(!canEdit)
            } else {
                Text("\(Int(org.alerts.tolerance * 100))% of the alert-after time").foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    private var longProcSection: some View {
        let own = host.longProc
        let orgRule = org.longProc
        let inherited = org.muted.long
        return Section {
            HStack(spacing: ThresholdColumns.spacing) {
                Text("Rule").frame(width: ThresholdColumns.name, alignment: .leading)
                sourceButton(isHost: own != nil) { toHost in
                    edit {
                        var rule = orgRule
                        rule.ignore = []
                        $0.longProc = toHost ? rule : nil
                    }
                }
                if own == nil {
                    Text(orgRule.enabled ? "\(Int(orgRule.cpuPct))% for \(SustainPicker.label(orgRule.sustainSeconds))" : "Off")
                        .foregroundStyle(.secondary)
                }
                Spacer()
                muteToggle(muted: host.muted.long, inherited: inherited) { on in
                    Task { await mute { try await rules.setLongMute(nodeID: nodeID, muted: on) } }
                }
            }
            if let own {
                LongProcRuleControls(rule: Binding(get: { host.longProc ?? own }, set: { new in
                    edit { $0.longProc = new }
                }))
                .disabled(!canEdit)
            }
            LabeledContent("Ignore on this host") {
                TagEditor(tags: Binding(get: { host.ignoredProcs }, set: { names in
                    edit { $0.ignoredProcs = names }
                }), prompt: "Add…")
                .disabled(!canEdit)
            }
            if !org.longProc.ignore.isEmpty {
                Text("Also ignored on all hosts: \(org.longProc.ignore.joined(separator: ", ")).")
                    .font(.caption).foregroundStyle(.secondary)
            }
        } header: {
            Text("Long-running processes")
        }
    }

    private func muteToggle(muted: Bool, inherited: Bool, set: @escaping (Bool) -> Void) -> some View {
        MuteToggle(
            muted: Binding(get: { muted }, set: set),
            inheritedMute: inherited,
            denied: !canMute
        )
    }

    // MARK: editing

    /// Applies a change to the draft and sends all host rules, one request
    /// after the other.
    private func edit(_ change: (inout RemoteHostRules) -> Void) {
        guard canEdit else { return }
        var next = host
        change(&next)
        if next.thresholds?.isEmpty == true { next.thresholds = nil }
        if next.alerts?.isEmpty == true { next.alerts = nil }
        draft = next
        inflight += 1
        let previous = chain
        let id = nodeID
        let rules = rules
        chain = Task { @MainActor in
            await previous?.value
            do {
                try await rules.saveHost(id, next)
                problem = nil
            } catch {
                problem = error.localizedDescription
                draft = rules.host(id)
            }
            inflight -= 1
        }
    }

    private func mute(_ send: () async throws -> Void) async {
        do {
            try await send()
            problem = nil
        } catch {
            problem = error.localizedDescription
        }
    }

    private static func with(_ o: ThresholdOverrides?, _ m: ThresholdMetric, _ pair: ThresholdPair?) -> ThresholdOverrides? {
        var out = o ?? ThresholdOverrides()
        out[m] = pair
        return out
    }

    private static func with(_ o: RemoteAlertOverrides?, _ metric: Metric, _ rule: AlertRule?) -> RemoteAlertOverrides? {
        var out = o ?? RemoteAlertOverrides()
        out.set(metric, rule)
        return out
    }

    static func notifyText(_ n: AlertNotify) -> String {
        switch n {
        case .off: return "Off"
        case .critical: return "Critical"
        case .all: return "Warn + Critical"
        }
    }
}

/// The org rules, read only. The web edits them.
struct CloudOrgRulesSummary: View {
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(CloudRulesMove.self) private var rulesMove: CloudRulesMove?

    var body: some View {
        if let cloud, let org = cloud.rules?.org {
            Section {
                ForEach(ThresholdMetric.usage) { m in
                    let p = org.thresholds[m]
                    LabeledContent(m.title, value: "\(m.format(p.warn)) / \(m.format(p.critical)) · after \(SustainPicker.label(org.alerts[m.alertMetric].sustainSeconds))")
                }
                LabeledContent("Long-running", value: org.longProc.enabled
                    ? "\(Int(org.longProc.cpuPct))% for \(SustainPicker.label(org.longProc.sustainSeconds))" : "Off")
                Button("Edit on the web") {
                    Task { await cloud.account.openDashboard(path: "settings") { NSWorkspace.shared.open($0) } }
                }
                .buttonStyle(.link)
                if let rulesMove, rulesMove.canMove {
                    Button("Move rules to cloud…") { Task { await rulesMove.open() } }
                        .buttonStyle(.link)
                }
            } header: {
                Text("Org rules in the cloud")
            } footer: {
                Text("A Cloud host uses these values until it has its own value for a metric.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }
}

extension RemoteAlertOverrides {
    func rule(_ metric: Metric) -> AlertRule? {
        switch metric {
        case .cpu: return cpu
        case .mem: return mem
        case .disk: return disk
        case .health: return health
        case .net: return nil
        }
    }

    mutating func set(_ metric: Metric, _ rule: AlertRule?) {
        switch metric {
        case .cpu: cpu = rule
        case .mem: mem = rule
        case .disk: disk = rule
        case .health: health = rule
        case .net: break
        }
    }
}
