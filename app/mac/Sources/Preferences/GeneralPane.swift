import SwiftUI

struct GeneralPane: View {
    @Environment(AppSettings.self) private var settings
    @State private var launchAtLoginError: String?

    var body: some View {
        @Bindable var settings = settings
        Form {
            Section("Polling") {
                HStack {
                    Text("Local")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(settings.localPollingIntervalSeconds) },
                            set: {
                                settings.localPollingIntervalSeconds = Int($0.rounded())
                                settings.persist()
                            }
                        ),
                        in: 1...60,
                        step: 1
                    )
                    Text("\(settings.localPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
                HStack {
                    Text("SSH")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(settings.sshPollingIntervalSeconds) },
                            set: {
                                settings.sshPollingIntervalSeconds = Int($0.rounded())
                                settings.persist()
                            }
                        ),
                        in: 1...300,
                        step: 1
                    )
                    Text("\(settings.sshPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
            }

            Section("Appearance") {
                Picker("Card density", selection: Binding(
                    get: { settings.cardDensity },
                    set: { settings.cardDensity = $0; settings.persist() }
                )) {
                    Text("Dense").tag(CardDensity.a)
                    Text("Relaxed").tag(CardDensity.b)
                }
                .pickerStyle(.segmented)
            }

            Section("Startup") {
                Toggle("Launch at login", isOn: Binding(
                    get: { settings.launchAtLogin },
                    set: { newValue in
                        let ok = LaunchAtLogin.shared.setEnabled(newValue)
                        if ok {
                            settings.launchAtLogin = newValue
                            launchAtLoginError = nil
                        } else {
                            launchAtLoginError = LaunchAtLogin.shared.lastError
                        }
                        settings.persist()
                    }
                ))
                if let err = launchAtLoginError {
                    Text(err)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }
        }
    }
}
