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
