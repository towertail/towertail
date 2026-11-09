import SwiftUI

/// Global notification policy, default thresholds and alert timing.
struct AlertsPane: View {
    /// Opens the Servers tab with this node selected.
    let onShowServer: (UUID) -> Void

    @Environment(ServerSettings.self) private var settings
    @Environment(ServerStore.self) private var serverStore
    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend
    @State private var showMoreHealth: Bool

    init(onShowServer: @escaping (UUID) -> Void, showMoreHealth: Bool = false) {
        self.onShowServer = onShowServer
        _showMoreHealth = State(initialValue: showMoreHealth)
    }

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
                Text("Memory goes critical at once under memory pressure. Without pressure, high memory only warns. The bell mutes a signal on all servers.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                timingRow("All health checks", rule: ruleBinding(.health))
                metricRow(.procs, rule: nil)
                metricRow(.zombies, rule: nil)
                DisclosureGroup("More checks", isExpanded: $showMoreHealth) {
                    ForEach(ThresholdMetric.moreHealth) { m in
                        if m == .memPressure {
                            healthRow(m, title: "Memory pressure · Linux PSI", help: "PSI avg10. Warns on some, goes critical on full.", note: nil)
                            healthRow(m, title: "Memory pressure · macOS", help: "Memory in use (100 − free %)",
                                      pair: macPressureBinding, note: nil)
                        } else {
                            healthRow(m, note: m == .inodes ? "At once" : nil)
                        }
                    }
                }
            } header: {
                sectionHeader("Host health", detail: "Process counts use a log scale.")
            } footer: {
                Text("On macOS, pressure is the share of memory in use.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                LongProcRuleControls(rule: binding(\.longProc))
                LabeledContent("Ignore on all hosts") {
                    TagEditor(tags: binding(\.longProc.ignore), prompt: "Add…")
                }
            } header: {
                sectionHeader("Long-running processes", detail: "One process with high CPU for a long time.")
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
                        // The ignore list is user data; keep it.
                        let ignore = settings.longProc.ignore
                        settings.longProc = .defaults
                        settings.longProc.ignore = ignore
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
            Text("Fleet now").lineLimit(1).frame(minWidth: 72, maxWidth: .infinity, alignment: .leading)
            Text("Warn").frame(width: ThresholdColumns.value)
            Text("Critical").frame(width: ThresholdColumns.value)
            Text("Alert after").frame(width: ThresholdColumns.sustain, alignment: .leading)
            Text("Notify").frame(width: ThresholdColumns.notify, alignment: .leading)
            Text("").frame(width: MuteToggle.width)
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
            bar.frame(minWidth: 72, maxWidth: .infinity)
            ThresholdPairFields(
                pair: pairBinding(m),
                scale: m.scale,
                unit: m.unit,
                range: m.range
            )
            timingControls(rule)
            muteToggle(m)
        }
    }

    private func timingRow(_ title: String, rule: Binding<AlertRule>) -> some View {
        HStack(spacing: ThresholdColumns.spacing) {
            Text(title).frame(width: ThresholdColumns.name, alignment: .leading)
            Spacer()
            timingControls(rule)
            Color.clear.frame(width: MuteToggle.width, height: 1)
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

    private func healthRow(_ m: ThresholdMetric, title: String? = nil, help: String? = nil,
                           pair: Binding<ThresholdPair>? = nil, note: String?) -> some View {
        HStack(spacing: ThresholdColumns.spacing) {
            Text(title ?? m.title)
                .help(help ?? m.help(hostOS: nil) ?? "")
            Spacer()
            ThresholdPairFields(pair: pair ?? pairBinding(m), scale: m.scale, unit: m.unit, range: m.range)
            Text(note ?? "")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: ThresholdColumns.sustain + ThresholdColumns.spacing + ThresholdColumns.notify, alignment: .leading)
            muteToggle(m)
        }
    }

    private func muteToggle(_ m: ThresholdMetric) -> some View {
        MuteToggle(
            muted: Binding(get: { settings.mutedSignals.contains(m) }, set: {
                settings.mutedSignals.set(m, muted: $0)
                settings.persist()
            }),
            unsupported: backend is RemoteBackend
        )
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
        nodeStore.nodes.filter {
            $0.thresholdOverrides != nil || $0.customAlerts != nil || $0.customLongProc != nil || !$0.mutedSignals.isEmpty
        }
    }

    private func overrideSummary(_ node: Node) -> String {
        var parts = (node.thresholdOverrides?.overridden ?? []).map { m in
            let p = node.thresholdOverrides![m]!
            return "\(m.title) \(m.format(p.warn)) / \(m.format(p.critical))"
        }
        if node.customAlerts != nil { parts.append("custom alert timing") }
        if node.customLongProc != nil { parts.append("custom long-running rule") }
        if !node.mutedSignals.isEmpty { parts.append("muted: " + node.mutedSignals.titles.joined(separator: ", ")) }
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

    private var macPressureBinding: Binding<ThresholdPair> {
        Binding(get: { settings.thresholds.health.macMemPressure }, set: {
            settings.thresholds.health.macMemPressure = $0
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
