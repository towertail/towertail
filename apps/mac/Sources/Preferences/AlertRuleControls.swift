import SwiftUI

/// How long a metric must stay over the line before it alerts.
struct SustainPicker: View {
    @Binding var seconds: Int

    private static let choices = [0, 30, 60, 120, 300, 600, 900, 1800, 3600]

    var body: some View {
        Picker("Alert after", selection: $seconds) {
            // A migrated value can fall between the presets; keep it selectable.
            ForEach(Self.choices.contains(seconds) ? Self.choices : (Self.choices + [seconds]).sorted(), id: \.self) { s in
                Text(Self.label(s)).tag(s)
            }
        }
        .help("How long the metric must stay over the line before it alerts. The card always shows the live value.")
    }

    static func label(_ seconds: Int) -> String {
        switch seconds {
        case 0: return "At once"
        case ..<60: return "\(seconds) s"
        case 3600: return "1 h"
        default:
            return seconds % 60 == 0 ? "\(seconds / 60) min" : "\(seconds / 60) min \(seconds % 60) s"
        }
    }
}

/// Which severities notify.
struct NotifyPicker: View {
    let title: String
    @Binding var notify: AlertNotify

    init(_ title: String = "Notify", notify: Binding<AlertNotify>) {
        self.title = title
        self._notify = notify
    }

    var body: some View {
        Picker(title, selection: $notify) {
            Text("Off").tag(AlertNotify.off)
            Text("Critical").tag(AlertNotify.critical)
            Text("Warn + Critical").tag(AlertNotify.all)
        }
    }
}

/// Fraction of the sustain window that must be over the line.
struct AlertToleranceControl: View {
    @Binding var tolerance: Double

    var body: some View {
        Picker("Over the line for at least", selection: $tolerance) {
            ForEach(choices, id: \.self) { v in
                Text(v == 1 ? "100% (no dips)" : "\(Int(v * 100))%").tag(v)
            }
        }
        .help("Share of the alert-after period the metric must be over the line. Below 100%, a short dip does not restart the timer.")
    }

    private var choices: [Double] {
        let presets = [0.6, 0.7, 0.8, 0.9, 1.0]
        return presets.contains(tolerance) ? presets : (presets + [tolerance]).sorted()
    }
}

extension AlertNotify {
    /// Maps a warn/critical flag pair to one level. Warn without critical
    /// shows as all, since critical is the more severe case.
    init(warn: Bool, critical: Bool) {
        switch (warn, critical) {
        case (false, false): self = .off
        case (false, true): self = .critical
        default: self = .all
        }
    }

    var notifiesWarn: Bool { self == .all }
    var notifiesCritical: Bool { self != .off }
}

/// Detect, CPU, duration and notify controls of a long-running process
/// rule. The ignore list is separate.
struct LongProcRuleControls: View {
    @Binding var rule: LongProcRule

    var body: some View {
        Toggle("Detect", isOn: field(\.enabled))
        Group {
            LabeledContent {
                HStack(spacing: 6) {
                    TextField("CPU", value: field(\.cpuPct), format: .number)
                        .labelsHidden()
                        .multilineTextAlignment(.trailing)
                        .frame(width: 60)
                    Text("% of one core").foregroundStyle(.secondary)
                }
            } label: {
                Text("CPU at or above")
                Text("100% is one full core, the same as top.")
            }
            LabeledContent {
                HStack(spacing: 6) {
                    TextField("Minutes", value: minutes, format: .number)
                        .labelsHidden()
                        .multilineTextAlignment(.trailing)
                        .frame(width: 60)
                    Text("minutes").foregroundStyle(.secondary)
                }
            } label: {
                Text("For at least")
                Text("Short dips (up to 20% of the time) do not reset the timer.")
            }
            Picker("Notify", selection: field(\.notify)) {
                Text("All").tag(AlertNotify.all)
                Text("Off").tag(AlertNotify.off)
            }
        }
        .disabled(!rule.enabled)
    }

    private var minutes: Binding<Int> {
        Binding(get: { rule.sustainSeconds / 60 }, set: { m in
            rule = rebuilt { $0.sustainSeconds = m * 60 }
        })
    }

    private func field<T>(_ path: WritableKeyPath<LongProcRule, T>) -> Binding<T> {
        Binding(get: { rule[keyPath: path] }, set: { v in
            rule = rebuilt { $0[keyPath: path] = v }
        })
    }

    /// Goes through the init, so the values stay in range.
    private func rebuilt(_ change: (inout LongProcRule) -> Void) -> LongProcRule {
        var r = rule
        change(&r)
        return LongProcRule(enabled: r.enabled, cpuPct: r.cpuPct, sustainSeconds: r.sustainSeconds, notify: r.notify, ignore: r.ignore)
    }
}
