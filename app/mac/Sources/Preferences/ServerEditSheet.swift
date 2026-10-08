import SwiftUI

struct ServerEditSheet: View {
    let context: ServerEditSheetContext
    let onSave: (Node) -> Void
    let onCancel: () -> Void

    @Environment(ServerSettings.self) private var serverSettings

    @State private var displayName: String
    @State private var kind: NodeKind
    @State private var sshUser: String
    @State private var sshHost: String
    @State private var sshPortText: String
    @State private var authMethod: AuthMethod
    @State private var password: String
    @State private var knownHostFingerprint: String?
    @State private var tagsString: String
    @State private var enabled: Bool
    @State private var iconOnWarn: Bool
    @State private var iconOnCritical: Bool
    @State private var notifyOnWarn: Bool
    @State private var notifyOnCritical: Bool
    @State private var useCustomThresholds: Bool
    @State private var thresholds: MetricThresholds
    @State private var useCustomAlerts: Bool
    @State private var alerts: AlertRules
    @State private var existingId: UUID?
    /// Snapshot of the on-disk authMethod when the sheet opens. Used to
    /// decide whether to delete the Keychain entry on save when the user
    /// flips from password → key.
    @State private var originalAuthMethod: AuthMethod
    @State private var showKeyHelp: Bool = false

    init(
        context: ServerEditSheetContext,
        onSave: @escaping (Node) -> Void,
        onCancel: @escaping () -> Void
    ) {
        self.context = context
        self.onSave = onSave
        self.onCancel = onCancel

        switch context {
        case .new:
            _displayName = State(initialValue: "")
            _kind = State(initialValue: .ssh)
            _sshUser = State(initialValue: NSUserName())
            _sshHost = State(initialValue: "")
            _sshPortText = State(initialValue: "")
            _authMethod = State(initialValue: .key)
            _password = State(initialValue: "")
            _knownHostFingerprint = State(initialValue: nil)
            _tagsString = State(initialValue: "")
            _enabled = State(initialValue: true)
            _iconOnWarn = State(initialValue: true)
            _iconOnCritical = State(initialValue: true)
            _notifyOnWarn = State(initialValue: true)
            _notifyOnCritical = State(initialValue: true)
            _useCustomThresholds = State(initialValue: false)
            _thresholds = State(initialValue: .defaults)
            _useCustomAlerts = State(initialValue: false)
            _alerts = State(initialValue: .defaults)
            _existingId = State(initialValue: nil)
            _originalAuthMethod = State(initialValue: .key)
        case .edit(let node):
            _displayName = State(initialValue: node.displayName)
            _kind = State(initialValue: node.kind)
            _sshUser = State(initialValue: node.sshUser ?? "")
            _sshHost = State(initialValue: node.sshHost ?? "")
            _sshPortText = State(initialValue: node.sshPort.map(String.init) ?? "")
            _authMethod = State(initialValue: node.authMethod)
            // Seed from Keychain on appear (below) so the field shows the
            // existing password and save-with-unchanged doesn't wipe it.
            _password = State(initialValue: "")
            _knownHostFingerprint = State(initialValue: node.knownHostFingerprint)
            _tagsString = State(initialValue: node.tags.joined(separator: ", "))
            _enabled = State(initialValue: node.enabled)
            _iconOnWarn = State(initialValue: node.iconOnWarn)
            _iconOnCritical = State(initialValue: node.iconOnCritical)
            _notifyOnWarn = State(initialValue: node.notifyOnWarn)
            _notifyOnCritical = State(initialValue: node.notifyOnCritical)
            _useCustomThresholds = State(initialValue: node.customThresholds != nil)
            _thresholds = State(initialValue: node.customThresholds ?? .defaults)
            _useCustomAlerts = State(initialValue: node.customAlerts != nil)
            _alerts = State(initialValue: node.customAlerts ?? .defaults)
            _existingId = State(initialValue: node.id)
            _originalAuthMethod = State(initialValue: node.authMethod)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(titleText)
                .font(.headline)

            Form {
                TextField("Name", text: $displayName)
                Picker("Kind", selection: $kind) {
                    Text("Local (this Mac)").tag(NodeKind.local)
                    Text("SSH").tag(NodeKind.ssh)
                }
                if kind == .ssh {
                    TextField("SSH user", text: $sshUser)
                    TextField("SSH host", text: $sshHost)
                    TextField("Port", text: $sshPortText, prompt: Text("22"))

                    Picker("Login method", selection: $authMethod) {
                        Text("SSH Key").tag(AuthMethod.key)
                        Text("Password").tag(AuthMethod.password)
                    }

                    if authMethod == .password {
                        SecureField("Password", text: $password)
                            .help("Stored in macOS Keychain, not in settings.json.")
                    }

                    Button("Need help setting up SSH keys?") {
                        showKeyHelp = true
                    }
                    .buttonStyle(.link)

                    if knownHostFingerprint != nil {
                        HStack {
                            Text("Host key")
                                .foregroundStyle(.secondary)
                            Text(knownHostFingerprint ?? "")
                                .font(.system(.caption, design: .monospaced))
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer()
                            Button("Forget") { knownHostFingerprint = nil }
                                .controlSize(.small)
                                .help("Clears the pinned fingerprint. The next connect will prompt again.")
                        }
                    }
                }
                TextField("Tags (comma-separated)", text: $tagsString)
                Toggle("Enabled", isOn: $enabled)

                Section("Menu-bar icon") {
                    Toggle("Color icon on warn", isOn: $iconOnWarn)
                        .help("When this server is in warn, tint the menu-bar icon yellow. Turn off for noisy but expected hosts.")
                    Toggle("Color icon on critical", isOn: $iconOnCritical)
                        .help("When this server is in critical, tint the menu-bar icon red.")
                }

                Section("Notifications") {
                    Toggle("Notify at warn threshold", isOn: $notifyOnWarn)
                    Toggle("Notify at critical threshold", isOn: $notifyOnCritical)
                }

                Section("Thresholds") {
                    Toggle("Customize thresholds for this server", isOn: $useCustomThresholds)
                        .help("When off, this server uses the global thresholds from the Thresholds tab.")

                    thresholdGroup(title: "CPU",
                                   warn: $thresholds.cpuWarn,
                                   critical: $thresholds.cpuCritical)
                    thresholdGroup(title: "Memory",
                                   warn: $thresholds.memWarn,
                                   critical: $thresholds.memCritical)
                    thresholdGroup(title: "Disk",
                                   warn: $thresholds.diskWarn,
                                   critical: $thresholds.diskCritical)
                }

                Section("Alerts") {
                    Toggle("Customize alerting for this server", isOn: $useCustomAlerts)
                        .help("When off, this server uses the global sustain and notify settings from the Thresholds tab.")
                    Group {
                        alertGroup(title: "CPU", rule: $alerts.cpu)
                        alertGroup(title: "Memory", rule: $alerts.mem)
                        alertGroup(title: "Disk", rule: $alerts.disk)
                        alertGroup(title: "Host health", rule: $alerts.health)
                        AlertToleranceControl(tolerance: $alerts.tolerance)
                    }
                    .disabled(!useCustomAlerts)
                }
            }

            HStack {
                Spacer()
                Button("Cancel", action: onCancel)
                Button("Save", action: save)
                    .keyboardShortcut(.defaultAction)
                    .disabled(!isValid)
            }
        }
        .padding(20)
        .frame(width: 520)
        .sheet(isPresented: $showKeyHelp) {
            SshKeySetupSheet(user: sshUser, host: sshHost) { showKeyHelp = false }
        }
        .onAppear {
            if case .new = context {
                thresholds = serverSettings.thresholds
            } else if case .edit(let node) = context, node.customThresholds == nil {
                thresholds = serverSettings.thresholds
            }
            if !useCustomAlerts {
                alerts = serverSettings.alertRules
            }
            // Seed password from Keychain so the field isn't blank on edit.
            if case .edit(let node) = context,
               node.authMethod == .password,
               password.isEmpty {
                password = (try? KeychainStore.getPassword(for: node.id)) ?? ""
            }
        }
    }

    @ViewBuilder
    private func thresholdGroup(title: String,
                                warn: Binding<Double>,
                                critical: Binding<Double>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.subheadline)
                .foregroundStyle(useCustomThresholds ? .primary : .secondary)
            thresholdRow(label: "Warn",
                         value: Binding(
                            get: { warn.wrappedValue },
                            set: { warn.wrappedValue = min($0, critical.wrappedValue) }
                         ))
            thresholdRow(label: "Critical",
                         value: Binding(
                            get: { critical.wrappedValue },
                            set: { critical.wrappedValue = max($0, warn.wrappedValue) }
                         ))
        }
        .disabled(!useCustomThresholds)
    }

    @ViewBuilder
    private func alertGroup(title: String, rule: Binding<AlertRule>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.subheadline)
                .foregroundStyle(useCustomAlerts ? .primary : .secondary)
            AlertRuleControls(rule: rule)
        }
    }

    @ViewBuilder
    private func thresholdRow(label: String, value: Binding<Double>) -> some View {
        HStack {
            Text(label).frame(width: 60, alignment: .leading)
            Slider(value: value, in: 0.1...0.99)
            Text("\(Int(round(value.wrappedValue * 100)))%")
                .font(.system(.body, design: .monospaced))
                .frame(width: 44, alignment: .trailing)
        }
    }

    private var titleText: String {
        switch context {
        case .new: return "New Server"
        case .edit: return "Edit Server"
        }
    }

    private var isValid: Bool {
        guard !displayName.trimmingCharacters(in: .whitespaces).isEmpty else { return false }
        if kind == .ssh {
            if sshUser.trimmingCharacters(in: .whitespaces).isEmpty
                || sshHost.trimmingCharacters(in: .whitespaces).isEmpty { return false }
            // If a port was supplied, it must be a valid 1-65535 number.
            let trimmed = sshPortText.trimmingCharacters(in: .whitespaces)
            if !trimmed.isEmpty {
                guard let p = Int(trimmed), (1...65535).contains(p) else { return false }
            }
        }
        return true
    }

    private func save() {
        let tags = tagsString
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let trimmedPort = sshPortText.trimmingCharacters(in: .whitespaces)
        let port: Int? = trimmedPort.isEmpty ? nil : Int(trimmedPort)
        let nodeId = existingId ?? UUID()
        let node = Node(
            id: nodeId,
            displayName: displayName.trimmingCharacters(in: .whitespaces),
            kind: kind,
            sshUser: kind == .ssh ? sshUser.trimmingCharacters(in: .whitespaces) : nil,
            sshHost: kind == .ssh ? sshHost.trimmingCharacters(in: .whitespaces) : nil,
            sshPort: kind == .ssh ? port : nil,
            authMethod: kind == .ssh ? authMethod : .key,
            knownHostFingerprint: kind == .ssh ? knownHostFingerprint : nil,
            tags: tags,
            enabled: enabled,
            iconOnWarn: iconOnWarn,
            iconOnCritical: iconOnCritical,
            notifyOnWarn: notifyOnWarn,
            notifyOnCritical: notifyOnCritical,
            customThresholds: useCustomThresholds ? thresholds : nil,
            customAlerts: useCustomAlerts ? alerts : nil
        )

        // Keychain handling: write on password auth, clear when the user
        // flips password → key (or away from SSH entirely).
        if kind == .ssh && authMethod == .password && !password.isEmpty {
            try? KeychainStore.setPassword(password, for: nodeId)
        } else if originalAuthMethod == .password && (authMethod != .password || kind != .ssh) {
            try? KeychainStore.deletePassword(for: nodeId)
        }

        onSave(node)
    }
}
