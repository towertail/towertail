import SwiftUI

/// Connection fields for a new server. Alert settings are edited later in
/// the server's Alerts section.
struct AddServerSheet: View {
    let onAdd: (Node) -> Void
    let onCancel: () -> Void

    @State private var displayName = ""
    @State private var kind: NodeKind = .ssh
    @State private var sshUser = NSUserName()
    @State private var sshHost = ""
    @State private var sshPortText = ""
    @State private var authMethod: AuthMethod = .key
    @State private var password = ""
    @State private var showKeyHelp = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Form {
                Section {
                    TextField("Name", text: $displayName)
                    Picker("Kind", selection: $kind) {
                        Text("SSH").tag(NodeKind.ssh)
                        Text("This Mac").tag(NodeKind.local)
                    }
                    .pickerStyle(.segmented)
                    if kind == .ssh {
                        LabeledContent("Address") {
                            HStack(spacing: 4) {
                                TextField("User", text: $sshUser).labelsHidden().frame(width: 90)
                                Text("@").foregroundStyle(.secondary)
                                TextField("Host", text: $sshHost, prompt: Text("host or IP")).labelsHidden()
                                Text(":").foregroundStyle(.secondary)
                                TextField("Port", text: $sshPortText, prompt: Text("22")).labelsHidden().frame(width: 52)
                            }
                        }
                        Picker("Sign in with", selection: $authMethod) {
                            Text("SSH key").tag(AuthMethod.key)
                            Text("Password").tag(AuthMethod.password)
                        }
                        .pickerStyle(.segmented)
                        if authMethod == .password {
                            SecureField("Password", text: $password)
                                .help("Stored in macOS Keychain, not in settings.json.")
                        } else {
                            LabeledContent("") {
                                Button("Key setup help") { showKeyHelp = true }
                                    .buttonStyle(.link)
                            }
                        }
                    }
                } header: {
                    Text("New Server")
                }
            }
            .formStyle(.grouped)
            .scrollDisabled(true)

            HStack {
                Spacer()
                Button("Cancel", action: onCancel)
                    .keyboardShortcut(.cancelAction)
                Button("Add", action: add)
                    .keyboardShortcut(.defaultAction)
                    .disabled(!isValid)
            }
            .padding([.horizontal, .bottom], 20)
        }
        .frame(width: 480)
        .fixedSize(horizontal: false, vertical: true)
        .sheet(isPresented: $showKeyHelp) {
            SshKeySetupSheet(user: sshUser, host: sshHost) { showKeyHelp = false }
        }
    }

    private var isValid: Bool {
        guard !displayName.trimmingCharacters(in: .whitespaces).isEmpty else { return false }
        guard kind == .ssh else { return true }
        if sshUser.trimmingCharacters(in: .whitespaces).isEmpty
            || sshHost.trimmingCharacters(in: .whitespaces).isEmpty { return false }
        let port = sshPortText.trimmingCharacters(in: .whitespaces)
        return port.isEmpty || Int(port).map { (1...65535).contains($0) } == true
    }

    private func add() {
        let port = sshPortText.trimmingCharacters(in: .whitespaces)
        let node = Node(
            displayName: displayName.trimmingCharacters(in: .whitespaces),
            kind: kind,
            sshUser: kind == .ssh ? sshUser.trimmingCharacters(in: .whitespaces) : nil,
            sshHost: kind == .ssh ? sshHost.trimmingCharacters(in: .whitespaces) : nil,
            sshPort: kind == .ssh && !port.isEmpty ? Int(port) : nil,
            authMethod: kind == .ssh ? authMethod : .key
        )
        if kind == .ssh, authMethod == .password, !password.isEmpty {
            try? KeychainStore.setPassword(password, for: node.id)
        }
        onAdd(node)
    }
}
