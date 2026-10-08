import SwiftUI

/// Sustain and notify pickers for one metric's alert rule.
struct AlertRuleControls: View {
    @Binding var rule: AlertRule

    private static let sustainChoices = [0, 30, 60, 120, 300, 600, 900, 1800, 3600]

    var body: some View {
        Picker("Sustain", selection: $rule.sustainSeconds) {
            // A migrated value can fall between the presets; keep it selectable.
            ForEach(choices, id: \.self) { s in
                Text(Self.label(s)).tag(s)
            }
        }
        .help("How long the metric must stay over the line before it alerts. The card always shows the live value.")
        Picker("Notify", selection: $rule.notify) {
            Text("Off").tag(AlertNotify.off)
            Text("Critical only").tag(AlertNotify.critical)
            Text("Warn + Critical").tag(AlertNotify.all)
        }
    }

    private var choices: [Int] {
        Self.sustainChoices.contains(rule.sustainSeconds)
            ? Self.sustainChoices
            : (Self.sustainChoices + [rule.sustainSeconds]).sorted()
    }

    static func label(_ seconds: Int) -> String {
        switch seconds {
        case 0: return "Immediately"
        case ..<60: return "\(seconds) s"
        case 3600: return "1 h"
        default:
            return seconds % 60 == 0 ? "\(seconds / 60) min" : "\(seconds / 60) min \(seconds % 60) s"
        }
    }
}

/// Fraction of the sustain window that must be over the line.
struct AlertToleranceControl: View {
    @Binding var tolerance: Double

    var body: some View {
        Picker("Tolerance", selection: $tolerance) {
            ForEach(choices, id: \.self) { v in
                Text(v == 1 ? "100% (no dips)" : "\(Int(v * 100))%").tag(v)
            }
        }
        .help("Share of the sustain period the metric must be over the line. Below 100%, a short dip does not restart the timer.")
    }

    private var choices: [Double] {
        let presets = [0.6, 0.7, 0.8, 0.9, 1.0]
        return presets.contains(tolerance) ? presets : (presets + [tolerance]).sorted()
    }
}
