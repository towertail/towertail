import SwiftUI

struct NotificationsPane: View {
    @Environment(AppSettings.self) private var settings

    var body: some View {
        @Bindable var settings = settings
        Form {
            Toggle("Enable notifications", isOn: Binding(
                get: { settings.notificationsEnabled },
                set: { settings.notificationsEnabled = $0; settings.persist() }
            ))

            Toggle("Warn-level alerts", isOn: Binding(
                get: { settings.notifyWarn },
                set: { settings.notifyWarn = $0; settings.persist() }
            ))
            .disabled(!settings.notificationsEnabled)

            Toggle("Critical alerts", isOn: Binding(
                get: { settings.notifyCritical },
                set: { settings.notifyCritical = $0; settings.persist() }
            ))
            .disabled(!settings.notificationsEnabled)

            HStack {
                Text("Debounce (s)")
                Stepper(
                    value: Binding(
                        get: { settings.notifyDebounceSeconds },
                        set: { settings.notifyDebounceSeconds = $0; settings.persist() }
                    ),
                    in: 0...600,
                    step: 10
                ) {
                    Text("\(settings.notifyDebounceSeconds)")
                        .font(.system(.body, design: .monospaced))
                }
            }
            .disabled(!settings.notificationsEnabled)
        }
        .safeAreaInset(edge: .bottom) {
            Text("Delivery lands in v1.1. Preferences are stored now.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.top, 8)
        }
    }
}
