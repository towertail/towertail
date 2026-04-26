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
