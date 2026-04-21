import SwiftUI

struct ServerEditSheet: View {
    let context: ServerEditSheetContext
    let onSave: (Node) -> Void
    let onCancel: () -> Void

    @Environment(AppSettings.self) private var appSettings

    @State private var displayName: String
    @State private var kind: NodeKind
    @State private var sshUser: String
    @State private var sshHost: String
    @State private var tagsString: String
    @State private var enabled: Bool
    @State private var iconOnWarn: Bool
    @State private var iconOnCritical: Bool
    @State private var notifyOnWarn: Bool
    @State private var notifyOnCritical: Bool
    @State private var useCustomThresholds: Bool
    @State private var thresholds: MetricThresholds
    @State private var existingId: UUID?

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
            _tagsString = State(initialValue: "")
            _enabled = State(initialValue: true)
            _iconOnWarn = State(initialValue: true)
            _iconOnCritical = State(initialValue: true)
            _notifyOnWarn = State(initialValue: true)
            _notifyOnCritical = State(initialValue: true)
            _useCustomThresholds = State(initialValue: false)
            // Seed with defaults; when the sheet appears the disabled sliders
            // will be replaced with the current global values via .onAppear.
            _thresholds = State(initialValue: .defaults)
            _existingId = State(initialValue: nil)
        case .edit(let node):
            _displayName = State(initialValue: node.displayName)
            _kind = State(initialValue: node.kind)
            _sshUser = State(initialValue: node.sshUser ?? "")
            _sshHost = State(initialValue: node.sshHost ?? "")
            _tagsString = State(initialValue: node.tags.joined(separator: ", "))
            _enabled = State(initialValue: node.enabled)
            _iconOnWarn = State(initialValue: node.iconOnWarn)
            _iconOnCritical = State(initialValue: node.iconOnCritical)
            _notifyOnWarn = State(initialValue: node.notifyOnWarn)
            _notifyOnCritical = State(initialValue: node.notifyOnCritical)
            _useCustomThresholds = State(initialValue: node.customThresholds != nil)
            _thresholds = State(initialValue: node.customThresholds ?? .defaults)
            _existingId = State(initialValue: node.id)
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
        .onAppear {
            // For a new node (or an existing one with no override yet) the
            // disabled-but-visible sliders should reflect the global values
            // the user is actually getting, so toggling the checkbox doesn't
            // jump to 75/90 defaults unrelated to their setup.
            if case .new = context {
                thresholds = appSettings.thresholds
            } else if case .edit(let node) = context, node.customThresholds == nil {
                thresholds = appSettings.thresholds
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
            return !sshUser.trimmingCharacters(in: .whitespaces).isEmpty
                && !sshHost.trimmingCharacters(in: .whitespaces).isEmpty
        }
        return true
    }

    private func save() {
        let tags = tagsString
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let node = Node(
            id: existingId ?? UUID(),
            displayName: displayName.trimmingCharacters(in: .whitespaces),
            kind: kind,
            sshUser: kind == .ssh ? sshUser.trimmingCharacters(in: .whitespaces) : nil,
            sshHost: kind == .ssh ? sshHost.trimmingCharacters(in: .whitespaces) : nil,
            tags: tags,
            enabled: enabled,
            iconOnWarn: iconOnWarn,
            iconOnCritical: iconOnCritical,
            notifyOnWarn: notifyOnWarn,
            notifyOnCritical: notifyOnCritical,
            customThresholds: useCustomThresholds ? thresholds : nil
        )
        onSave(node)
    }
}
