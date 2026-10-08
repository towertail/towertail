import SwiftUI

struct ThresholdsPane: View {
    @Environment(ServerSettings.self) private var settings

    var body: some View {
        @Bindable var settings = settings
        Form {
            Section("CPU") {
                thresholdRow(
                    warn: Binding(get: { settings.thresholds.cpuWarn }, set: {
                        settings.thresholds.cpuWarn = min($0, settings.thresholds.cpuCritical)
                        settings.persist()
                    }),
                    critical: Binding(get: { settings.thresholds.cpuCritical }, set: {
                        settings.thresholds.cpuCritical = max($0, settings.thresholds.cpuWarn)
                        settings.persist()
                    })
                )
                AlertRuleControls(rule: alertBinding(\.cpu))
            }
            Section {
                thresholdRow(
                    warn: Binding(get: { settings.thresholds.memWarn }, set: {
                        settings.thresholds.memWarn = min($0, settings.thresholds.memCritical)
                        settings.persist()
                    }),
                    critical: Binding(get: { settings.thresholds.memCritical }, set: {
                        settings.thresholds.memCritical = max($0, settings.thresholds.memWarn)
                        settings.persist()
                    })
                )
                AlertRuleControls(rule: alertBinding(\.mem))
            } header: {
                Text("Memory")
            } footer: {
                Text("Critical fires at once under memory pressure (PSI, macOS pressure, or fast swap growth). Without pressure, high memory only warns.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Section {
                countRow("Processes",
                    warn: Binding(get: { settings.thresholds.procsWarn }, set: {
                        settings.thresholds.procsWarn = max(1, min($0, settings.thresholds.procsCritical))
                        settings.persist()
                    }),
                    critical: Binding(get: { settings.thresholds.procsCritical }, set: {
                        settings.thresholds.procsCritical = max($0, settings.thresholds.procsWarn)
                        settings.persist()
                    })
                )
                countRow("Zombies",
                    warn: Binding(get: { settings.thresholds.zombiesWarn }, set: {
                        settings.thresholds.zombiesWarn = max(1, min($0, settings.thresholds.zombiesCritical))
                        settings.persist()
                    }),
                    critical: Binding(get: { settings.thresholds.zombiesCritical }, set: {
                        settings.thresholds.zombiesCritical = max($0, settings.thresholds.zombiesWarn)
                        settings.persist()
                    })
                )
                AlertRuleControls(rule: alertBinding(\.health))
            } header: {
                Text("Host health")
            } footer: {
                Text("Also checked: PID use 70/90%, open files 80/95%, inodes 85/95%, memory pressure 10/20%, I/O pressure 20/40%. Inodes alert at once.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Section("Disk") {
                thresholdRow(
                    warn: Binding(get: { settings.thresholds.diskWarn }, set: {
                        settings.thresholds.diskWarn = min($0, settings.thresholds.diskCritical)
                        settings.persist()
                    }),
                    critical: Binding(get: { settings.thresholds.diskCritical }, set: {
                        settings.thresholds.diskCritical = max($0, settings.thresholds.diskWarn)
                        settings.persist()
                    })
                )
                AlertRuleControls(rule: alertBinding(\.disk))
            }
            Section {
                AlertToleranceControl(tolerance: Binding(get: { settings.alertRules.tolerance }, set: {
                    settings.alertRules.tolerance = $0
                    settings.persist()
                }))
            } header: {
                Text("Alerts")
            } footer: {
                Text("Sustain and notify settings control notifications and the menu-bar icon. Cards always show the live value.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .safeAreaInset(edge: .bottom) {
            HStack {
                Button("Reset to defaults") {
                    settings.thresholds = .defaults
                    settings.alertRules = .defaults
                    settings.persist()
                }
                Spacer()
                Text("Applied globally across all hosts.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 4)
            .padding(.top, 8)
        }
    }

    @ViewBuilder
    private func countRow(_ label: String, warn: Binding<Int>, critical: Binding<Int>) -> some View {
        HStack {
            Text(label).frame(width: 80, alignment: .leading)
            Text("Warn").foregroundStyle(.secondary)
            TextField("", value: warn, format: .number)
                .frame(width: 80)
                .multilineTextAlignment(.trailing)
            Text("Critical").foregroundStyle(.secondary)
            TextField("", value: critical, format: .number)
                .frame(width: 80)
                .multilineTextAlignment(.trailing)
        }
    }

    private func alertBinding(_ path: WritableKeyPath<AlertRules, AlertRule>) -> Binding<AlertRule> {
        Binding(get: { settings.alertRules[keyPath: path] }, set: {
            settings.alertRules[keyPath: path] = $0
            settings.persist()
        })
    }

    @ViewBuilder
    private func thresholdRow(warn: Binding<Double>, critical: Binding<Double>) -> some View {
        HStack {
            Text("Warn").frame(width: 60, alignment: .leading)
            Slider(value: warn, in: 0.1...0.99)
            Text("\(Int(round(warn.wrappedValue * 100)))%")
                .font(.system(.body, design: .monospaced))
                .frame(width: 48, alignment: .trailing)
        }
        HStack {
            Text("Critical").frame(width: 60, alignment: .leading)
            Slider(value: critical, in: 0.1...0.99)
            Text("\(Int(round(critical.wrappedValue * 100)))%")
                .font(.system(.body, design: .monospaced))
                .frame(width: 48, alignment: .trailing)
        }
    }
}
