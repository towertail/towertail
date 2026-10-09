import SwiftUI

/// Global notification policy, default thresholds and alert timing.
struct AlertsPane: View {
    /// Opens the Servers tab with this node selected.
    let onShowServer: (UUID) -> Void

    @Environment(ServerSettings.self) private var settings
    @Environment(ServerStore.self) private var serverStore
    @Environment(NodeStore.self) private var nodeStore
    @State private var showMoreHealth = false

    private static let repeatChoices = [0, 30, 60, 300, 900, 1800, 3600]

    var body: some View {
        Form {
            Section("Notifications") {
                Picker("Notify on", selection: globalNotify) {
                    Text("Off").tag(AlertNotify.off)
                    Text("Critical").tag(AlertNotify.critical)
                    Text("Warn + Critical").tag(AlertNotify.all)
                }
                .pickerStyle(.segmented)
                Picker("Repeat the same alert at most every", selection: binding(\.notifyDebounceSeconds)) {
                    ForEach(repeatChoices, id: \.self) { s in
                        Text(s == 0 ? "No limit" : SustainPicker.label(s)).tag(s)
                    }
                }
                .disabled(globalNotify.wrappedValue == .off)
            }

            Section {
                columnHeader
                ForEach([ThresholdMetric.cpu, .mem, .disk]) { m in
                    metricRow(m, rule: ruleBinding(m.alertMetric))
                }
            } header: {
                sectionHeader("Default thresholds", detail: "Every server uses these unless it overrides a metric.")
            } footer: {
                Text("Memory goes critical at once under memory pressure. Without pressure, high memory only warns.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                timingRow("All health checks", rule: ruleBinding(.health))
                metricRow(.procs, rule: nil)
                metricRow(.zombies, rule: nil)
                DisclosureGroup("More checks", isExpanded: $showMoreHealth) {
                    ForEach(ThresholdMetric.moreHealth) { m in
                        healthRow(m, note: m == .inodes ? "At once" : nil)
                    }
                }
            } header: {
                sectionHeader("Host health", detail: "Process counts use a log scale.")
            }

            Section {
                AlertToleranceControl(tolerance: Binding(get: { settings.alertRules.tolerance }, set: {
                    settings.alertRules.tolerance = $0
                    settings.persist()
                }))
            } header: {
                Text("Alert timing")
            } footer: {
                Text("Alert rules control notifications and the menu-bar icon. Cards always show the live value.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                if overriddenNodes.isEmpty {
                    Text("No server overrides the defaults.")
                        .foregroundStyle(.secondary)
                }
                ForEach(overriddenNodes) { node in
                    HStack(spacing: 8) {
                        Circle()
                            .fill(serverStore.serverVMs.first(where: { $0.id == node.id })?.statusDotColor ?? .secondary)
                            .frame(width: 8, height: 8)
                        Text(node.displayName)
                            .frame(width: ThresholdColumns.name, alignment: .leading)
                        Text(overrideSummary(node))
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        Spacer()
                        Button("Show") { onShowServer(node.id) }
                            .buttonStyle(.link)
                    }
                }
            } header: {
                sectionHeader("Server overrides", detail: "Edit these on the server itself.")
            } footer: {
                HStack {
                    Button("Restore Defaults") {
                        settings.thresholds = .defaults
                        settings.alertRules = .defaults
                        settings.persist()
                    }
                    Spacer()
                }
                .padding(.top, 6)
            }
        }
        .formStyle(.grouped)
    }

    // MARK: rows

    private var columnHeader: some View {
        HStack(spacing: ThresholdColumns.spacing) {
            Text("Metric").frame(width: ThresholdColumns.name, alignment: .leading)
            Text("Fleet now").frame(maxWidth: .infinity, alignment: .leading)
            Text("Warn").frame(width: ThresholdColumns.value)
            Text("Critical").frame(width: ThresholdColumns.value)
            Text("Alert after").frame(width: ThresholdColumns.sustain, alignment: .leading)
            Text("Notify").frame(width: ThresholdColumns.notify, alignment: .leading)
        }
        .font(.caption)
        .foregroundStyle(.secondary)
    }

    private func metricRow(_ m: ThresholdMetric, rule: Binding<AlertRule>?) -> some View {
        let bar = FleetBar(metric: m, pair: settings.thresholds[m], points: fleetPoints(m))
        let summary = bar.summary
        return HStack(spacing: ThresholdColumns.spacing) {
            VStack(alignment: .leading, spacing: 1) {
                Text(m.title)
                Text(summary.text)
                    .font(.caption)
                    .foregroundStyle(summary.tint == .stale ? Color.secondary : summary.tint.color)
            }
            .frame(width: ThresholdColumns.name, alignment: .leading)
            bar.frame(maxWidth: .infinity)
            ThresholdPairFields(
                pair: pairBinding(m),
                scale: m.scale,
                unit: m.unit,
                range: m.range
            )
            timingControls(rule)
        }
    }

    private func timingRow(_ title: String, rule: Binding<AlertRule>) -> some View {
        HStack(spacing: ThresholdColumns.spacing) {
            Text(title).frame(width: ThresholdColumns.name, alignment: .leading)
            Spacer()
            timingControls(rule)
        }
    }

    @ViewBuilder
    private func timingControls(_ rule: Binding<AlertRule>?) -> some View {
        if let rule {
            SustainPicker(seconds: rule.sustainSeconds)
                .labelsHidden()
                .frame(width: ThresholdColumns.sustain)
            NotifyPicker(notify: rule.notify)
                .labelsHidden()
                .frame(width: ThresholdColumns.notify)
        } else {
            Color.clear.frame(width: ThresholdColumns.sustain + ThresholdColumns.spacing + ThresholdColumns.notify, height: 1)
        }
    }

    private func healthRow(_ m: ThresholdMetric, note: String?) -> some View {
        HStack(spacing: ThresholdColumns.spacing) {
            Text(m.title)
                .help(m.help ?? "")
            Spacer()
            ThresholdPairFields(pair: pairBinding(m), scale: m.scale, unit: m.unit, range: m.range)
            Text(note ?? "")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: ThresholdColumns.sustain + ThresholdColumns.spacing + ThresholdColumns.notify, alignment: .leading)
        }
    }

    private func sectionHeader(_ title: String, detail: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title)
            Spacer()
            Text(detail)
                .font(.caption)
                .fontWeight(.regular)
                .foregroundStyle(.secondary)
        }
    }

    // MARK: data

    /// Live values from enabled servers that use the default for this metric.
    private func fleetPoints(_ m: ThresholdMetric) -> [FleetPoint] {
        let nodes = Dictionary(uniqueKeysWithValues: nodeStore.nodes.map { ($0.id, $0) })
        return serverStore.serverVMs.compactMap { vm in
            guard let node = nodes[vm.id], node.enabled, node.thresholdOverrides?[m] == nil else { return nil }
            switch vm.state {
            case .online, .warn, .critical: break
            default: return nil
            }
            let v: Double?
            switch m {
            case .cpu: v = vm.cpu.latest?.v
            case .mem: v = vm.mem.latest?.v
            case .disk: v = vm.disk.latest?.v
            case .procs: v = vm.health.info.map { Double($0.procs) }
            case .zombies: v = vm.health.info?.zombies.map(Double.init)
            default: v = nil
            }
            return v.map { FleetPoint(id: vm.id, name: node.displayName, value: $0) }
        }
    }

    private var overriddenNodes: [Node] {
        nodeStore.nodes.filter { $0.thresholdOverrides != nil || $0.customAlerts != nil }
    }

    private func overrideSummary(_ node: Node) -> String {
        var parts = (node.thresholdOverrides?.overridden ?? []).map { m in
            let p = node.thresholdOverrides![m]!
            return "\(m.title) \(m.format(p.warn)) / \(m.format(p.critical))"
        }
        if node.customAlerts != nil { parts.append("custom alert timing") }
        return parts.joined(separator: ", ")
    }

    // MARK: bindings

    private var repeatChoices: [Int] {
        let s = settings.notifyDebounceSeconds
        return Self.repeatChoices.contains(s) ? Self.repeatChoices : (Self.repeatChoices + [s]).sorted()
    }

    private var globalNotify: Binding<AlertNotify> {
        Binding(get: {
            settings.notificationsEnabled ? AlertNotify(warn: settings.notifyWarn, critical: settings.notifyCritical) : .off
        }, set: {
            settings.notificationsEnabled = $0 != .off
            if $0 != .off {
                settings.notifyWarn = $0.notifiesWarn
                settings.notifyCritical = $0.notifiesCritical
            }
            settings.persist()
        })
    }

    private func binding<T>(_ path: ReferenceWritableKeyPath<ServerSettings, T>) -> Binding<T> {
        Binding(get: { settings[keyPath: path] }, set: {
            settings[keyPath: path] = $0
            settings.persist()
        })
    }

    private func pairBinding(_ m: ThresholdMetric) -> Binding<ThresholdPair> {
        Binding(get: { settings.thresholds[m] }, set: {
            settings.thresholds[m] = $0
            settings.persist()
        })
    }

    private func ruleBinding(_ metric: Metric) -> Binding<AlertRule> {
        Binding(get: { settings.alertRules[metric] }, set: {
            settings.alertRules[metric] = $0
            settings.persist()
        })
    }
}
