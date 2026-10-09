import SwiftUI

/// Settings for one server. Edits apply at once: toggles and pickers on
/// change, text fields when they lose focus or on Return.
struct ServerDetailView: View {
    let tester: ServerTester
    /// Opens the Alerts tab. Nil hides the link.
    var onEditDefaults: (() -> Void)?

    @Environment(NodeStore.self) private var nodeStore
    @Environment(ServerStore.self) private var serverStore
    @Environment(ServerSettings.self) private var settings
    @Environment(ClientSettings.self) private var clientSettings
    @Environment(SamplerUpdateCoordinator.self) private var samplerUpdater
    @Environment(\.backend) private var backend

    @State private var draft: Node
    @State private var portText: String
    @State private var password = ""
    @State private var savedPassword = ""
    @State private var tab: Tab = .connection
    @State private var showKeyHelp = false
    @State private var terminalError: String?
    @FocusState private var focus: Field?

    enum Tab: Hashable { case connection, alerts }
    private enum Field: Hashable { case name, user, host, port, password }

    init(node: Node, tester: ServerTester, onEditDefaults: (() -> Void)? = nil) {
        self.tester = tester
        self.onEditDefaults = onEditDefaults
        _draft = State(initialValue: node)
        _portText = State(initialValue: node.sshPort.map(String.init) ?? "")
    }

    var body: some View {
        VStack(spacing: 0) {
            header
                .padding(.horizontal, 20)
                .padding(.top, 16)
            Picker("Section", selection: $tab) {
                Text("Connection").tag(Tab.connection)
                Text("Alerts").tag(Tab.alerts)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            .padding(.top, 12)
            switch tab {
            case .connection: connectionForm
            case .alerts: alertsForm
            }
        }
        .onChange(of: focus) { old, _ in
            if old != nil { commit() }
        }
        .onDisappear(perform: commit)
        .onAppear {
            if draft.authMethod == .password {
                savedPassword = (try? KeychainStore.getPassword(for: draft.id)) ?? ""
                password = savedPassword
            }
        }
        .sheet(isPresented: $showKeyHelp) {
            SshKeySetupSheet(user: draft.sshUser ?? "", host: draft.sshHost ?? "") { showKeyHelp = false }
        }
        .alert("Could not open terminal", isPresented: Binding(get: { terminalError != nil }, set: { if !$0 { terminalError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(terminalError ?? "")
        }
    }

    // MARK: header

    private var vm: ServerViewModel? { serverStore.serverVMs.first { $0.id == draft.id } }

    private var header: some View {
        HStack(spacing: 12) {
            Image(systemName: draft.kind == .local ? "laptopcomputer" : "server.rack")
                .font(.system(size: 20))
                .foregroundStyle(.secondary)
                .frame(width: 40, height: 40)
                .background(RoundedRectangle(cornerRadius: 9).fill(.background))
            VStack(alignment: .leading, spacing: 2) {
                Text(draft.displayName.isEmpty ? "Untitled" : draft.displayName)
                    .font(.title3.weight(.semibold))
                    .lineLimit(1)
                Text(statusLine)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            Button("Test") { tester.test([draft], backend: backend, nodeStore: nodeStore) }
                .disabled(tester.running)
            if draft.kind == .ssh {
                Button("Open SSH") {
                    if case .failure(let err) = TerminalLauncher.openSSH(for: draft, app: clientSettings.defaultTerminalApp) {
                        terminalError = err.localizedDescription
                    }
                }
            }
        }
    }

    private var statusLine: String {
        guard draft.enabled else { return "Paused" }
        guard let vm else { return "—" }
        let state: String
        switch vm.state {
        case .unknown: state = "Connecting"
        case .online: state = "Online"
        case .warn: state = "Warn"
        case .critical: state = "Critical"
        case .offline(let reason): state = "Offline: \(reason)"
        case .suspended(let reason): state = "Paused: \(reason)"
        }
        return [state, vm.osArch].filter { !$0.isEmpty && $0 != "—" }.joined(separator: " · ")
    }

    // MARK: connection

    private var connectionForm: some View {
        Form {
            Section {
                TextField("Name", text: $draft.displayName)
                    .focused($focus, equals: .name)
                    .onSubmit(commit)
                if draft.kind == .ssh {
                    LabeledContent("Address") {
                        HStack(spacing: 4) {
                            TextField("User", text: optionalText(\.sshUser))
                                .labelsHidden()
                                .frame(width: 90)
                                .focused($focus, equals: .user)
                            Text("@").foregroundStyle(.secondary)
                            TextField("Host", text: optionalText(\.sshHost))
                                .labelsHidden()
                                .focused($focus, equals: .host)
                            Text(":").foregroundStyle(.secondary)
                            TextField("Port", text: $portText, prompt: Text("22"))
                                .labelsHidden()
                                .frame(width: 52)
                                .focused($focus, equals: .port)
                        }
                        .onSubmit(commit)
                    }
                    if let problem = addressProblem {
                        Text(problem)
                            .font(.caption)
                            .foregroundStyle(ThresholdTint.critical.color)
                    }
                    Picker("Sign in with", selection: Binding(get: { draft.authMethod }, set: {
                        draft.authMethod = $0
                        if $0 == .key { password = "" }
                        commit()
                    })) {
                        Text("SSH key").tag(AuthMethod.key)
                        Text("Password").tag(AuthMethod.password)
                    }
                    .pickerStyle(.segmented)
                    if draft.authMethod == .password {
                        SecureField("Password", text: $password)
                            .focused($focus, equals: .password)
                            .onSubmit(commit)
                            .help("Stored in macOS Keychain, not in settings.json.")
                    } else {
                        LabeledContent("") {
                            Button("Key setup help") { showKeyHelp = true }
                                .buttonStyle(.link)
                        }
                    }
                } else {
                    LabeledContent("Address", value: "This Mac")
                }
            }

            Section {
                LabeledContent("Tags") {
                    TagEditor(tags: Binding(get: { draft.tags }, set: { draft.tags = $0; commit() }))
                }
                Toggle("Monitor this server", isOn: Binding(get: { draft.enabled }, set: { draft.enabled = $0; commit() }))
            }

            if draft.kind == .ssh {
                Section {
                    LabeledContent("Sampler") {
                        HStack(spacing: 8) {
                            samplerStatus
                            Button("Reinstall") { tester.reinstall([draft]) }
                                .disabled(tester.running)
                                .help("Force-upload the bundled sampler to ~/.towertail/towertail-sampler.")
                        }
                    }
                    if let fp = draft.knownHostFingerprint {
                        LabeledContent("Host key") {
                            HStack(spacing: 8) {
                                Text(fp)
                                    .font(.system(.caption, design: .monospaced))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                    .textSelection(.enabled)
                                Button("Forget") {
                                    draft.knownHostFingerprint = nil
                                    commit()
                                }
                                .help("Clears the pinned fingerprint. The next connect prompts again.")
                            }
                        }
                    }
                } footer: {
                    Text("Changes apply at once.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
    }

    @ViewBuilder
    private var samplerStatus: some View {
        if samplerUpdater.isUpdating(id: draft.id) {
            ProgressView().controlSize(.mini)
            Text("Updating…").foregroundStyle(.secondary)
        } else {
            Text(vm?.samplerVersion.isEmpty == false ? vm!.samplerVersion : "—")
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(.secondary)
            if let err = samplerUpdater.lastUpdateError[draft.id] {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(ThresholdTint.warn.color)
                    .help("Auto-update failed: \(err)")
            }
        }
    }

    // MARK: alerts

    private var alertsForm: some View {
        Form {
            Section {
                ForEach(ThresholdMetric.usage) { m in
                    overrideRow(m)
                }
            } header: {
                HStack {
                    Text("Thresholds")
                    Spacer()
                    if let onEditDefaults {
                        Button("Edit defaults", action: onEditDefaults)
                            .buttonStyle(.link)
                            .font(.caption)
                    }
                }
            }

            Section("Host health") {
                ForEach(ThresholdMetric.health) { m in
                    overrideRow(m)
                }
            }

            Section {
                Toggle("Custom alert timing", isOn: Binding(get: { draft.customAlerts != nil }, set: {
                    draft.customAlerts = $0 ? settings.alertRules : nil
                    commit()
                }))
                .help("When off, this server uses the alert timing from the Alerts tab.")
                if draft.customAlerts != nil {
                    timingRow("CPU", .cpu)
                    timingRow("Memory", .mem)
                    timingRow("Disk", .disk)
                    timingRow("Host health", .health)
                    AlertToleranceControl(tolerance: Binding(get: { draft.customAlerts?.tolerance ?? 1 }, set: {
                        draft.customAlerts?.tolerance = $0
                        commit()
                    }))
                }
            }

            Section {
                Picker("Notify on", selection: Binding(
                    get: { AlertNotify(warn: draft.notifyOnWarn, critical: draft.notifyOnCritical) },
                    set: { draft.notifyOnWarn = $0.notifiesWarn; draft.notifyOnCritical = $0.notifiesCritical; commit() }
                )) {
                    Text("Off").tag(AlertNotify.off)
                    Text("Critical").tag(AlertNotify.critical)
                    Text("Warn + Critical").tag(AlertNotify.all)
                }
                .pickerStyle(.segmented)
                Picker("Tint menu-bar icon on", selection: Binding(
                    get: { AlertNotify(warn: draft.iconOnWarn, critical: draft.iconOnCritical) },
                    set: { draft.iconOnWarn = $0.notifiesWarn; draft.iconOnCritical = $0.notifiesCritical; commit() }
                )) {
                    Text("Off").tag(AlertNotify.off)
                    Text("Critical").tag(AlertNotify.critical)
                    Text("Warn + Critical").tag(AlertNotify.all)
                }
                .pickerStyle(.segmented)
            } header: {
                Text("For this server")
            } footer: {
                Text("Use Critical for noisy hosts that often sit in warn.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }

    private func overrideRow(_ m: ThresholdMetric) -> some View {
        let global = settings.thresholds[m]
        let defaultText = "\(m.format(global.warn)) / \(m.format(global.critical))"
        let override = draft.thresholdOverrides?[m]
        return HStack(spacing: 10) {
            Text(m.title)
                .frame(width: 116, alignment: .leading)
                .help(m.help ?? "")
            if override != nil {
                ThresholdPairFields(
                    pair: Binding(get: { draft.thresholdOverrides?[m] ?? global }, set: { setOverride(m, $0) }),
                    scale: m.scale,
                    unit: m.unit,
                    range: m.range
                )
                Text("default \(defaultText)")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            } else {
                Text(defaultText).foregroundStyle(.secondary)
                Text("default").font(.caption).foregroundStyle(.tertiary)
            }
            Spacer()
            if override != nil {
                Button("Revert") { setOverride(m, nil) }
                    .buttonStyle(.link)
            } else {
                Button("Override") { setOverride(m, global) }
                    .controlSize(.small)
            }
        }
    }

    private func timingRow(_ title: String, _ metric: Metric) -> some View {
        HStack {
            Text(title)
            Spacer()
            SustainPicker(seconds: Binding(get: { draft.customAlerts?[metric].sustainSeconds ?? 0 }, set: {
                draft.customAlerts?[metric].sustainSeconds = $0
                commit()
            }))
            .labelsHidden()
            .frame(width: ThresholdColumns.sustain)
            NotifyPicker(notify: Binding(get: { draft.customAlerts?[metric].notify ?? .off }, set: {
                draft.customAlerts?[metric].notify = $0
                commit()
            }))
            .labelsHidden()
            .frame(width: ThresholdColumns.notify)
        }
    }

    // MARK: editing

    private func setOverride(_ m: ThresholdMetric, _ pair: ThresholdPair?) {
        var o = draft.thresholdOverrides ?? ThresholdOverrides()
        o[m] = pair
        draft.thresholdOverrides = o.isEmpty ? nil : o
        commit()
    }

    private func optionalText(_ path: WritableKeyPath<Node, String?>) -> Binding<String> {
        Binding(get: { draft[keyPath: path] ?? "" }, set: { draft[keyPath: path] = $0 })
    }

    private var trimmedPort: String { portText.trimmingCharacters(in: .whitespaces) }

    private var addressProblem: String? {
        guard draft.kind == .ssh else { return nil }
        if (draft.sshUser ?? "").trimmingCharacters(in: .whitespaces).isEmpty { return "Enter an SSH user." }
        if (draft.sshHost ?? "").trimmingCharacters(in: .whitespaces).isEmpty { return "Enter a host." }
        if !trimmedPort.isEmpty, Int(trimmedPort).map({ (1...65535).contains($0) }) != true { return "Port must be 1 to 65535." }
        return nil
    }

    /// Writes the draft onto the stored node. Starts from the stored copy so
    /// fields this view does not edit (snooze, favorite) stay current.
    private func commit() {
        guard var n = nodeStore.node(withId: draft.id) else { return }
        let name = draft.displayName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty, addressProblem == nil else { return }
        let stored = n
        n.displayName = name
        if n.kind == .ssh {
            n.sshUser = draft.sshUser?.trimmingCharacters(in: .whitespaces)
            n.sshHost = draft.sshHost?.trimmingCharacters(in: .whitespaces)
            n.sshPort = trimmedPort.isEmpty ? nil : Int(trimmedPort)
            n.authMethod = draft.authMethod
            n.knownHostFingerprint = draft.knownHostFingerprint
        }
        n.tags = draft.tags
        n.enabled = draft.enabled
        n.iconOnWarn = draft.iconOnWarn
        n.iconOnCritical = draft.iconOnCritical
        n.notifyOnWarn = draft.notifyOnWarn
        n.notifyOnCritical = draft.notifyOnCritical
        n.thresholdOverrides = draft.thresholdOverrides
        n.customAlerts = draft.customAlerts

        var passwordChanged = false
        if n.kind == .ssh, n.authMethod == .password, !password.isEmpty, password != savedPassword {
            try? KeychainStore.setPassword(password, for: n.id)
            savedPassword = password
            passwordChanged = true
        } else if stored.authMethod == .password, n.authMethod != .password {
            try? KeychainStore.deletePassword(for: n.id)
            savedPassword = ""
        }

        guard n != stored || passwordChanged else { return }
        let b = backend
        let node = n
        Task {
            try? await b?.updateNode(node)
            // A new password does not change the node, so restart its pacer.
            if passwordChanged, node == stored { await b?.respawnPacer(id: node.id) }
        }
    }
}
