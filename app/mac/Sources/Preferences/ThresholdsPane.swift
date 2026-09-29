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
                sustainRow(
                    samples: Binding(get: { settings.thresholds.cpuSustainSamples }, set: {
                        settings.thresholds.cpuSustainSamples = max(1, $0)
                        settings.persist()
                    })
                )
            }
            Section("Memory") {
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
                sustainRow(
                    samples: Binding(get: { settings.thresholds.memSustainSamples }, set: {
                        settings.thresholds.memSustainSamples = max(1, $0)
                        settings.persist()
                    })
                )
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
            } header: {
                Text("Host health")
            } footer: {
                Text("Also checked: PID use 70/90%, open files 80/95%, inodes 85/95%, memory pressure 10/20%, I/O pressure 20/40%.")
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
                sustainRow(
                    samples: Binding(get: { settings.thresholds.diskSustainSamples }, set: {
                        settings.thresholds.diskSustainSamples = max(1, $0)
                        settings.persist()
                    })
                )
            }
        }
        .safeAreaInset(edge: .bottom) {
            HStack {
                Button("Reset to defaults") {
                    settings.thresholds = .defaults
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

    @ViewBuilder
    private func sustainRow(samples: Binding<Int>) -> some View {
        HStack {
            Text("Sustain").frame(width: 60, alignment: .leading)
            Stepper(value: samples, in: 1...30) {
                let s = samples.wrappedValue
                Text(s == 1 ? "Trigger immediately" : "After \(s) consecutive samples")
                    .foregroundStyle(.secondary)
                    .font(.callout)
            }
        }
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
