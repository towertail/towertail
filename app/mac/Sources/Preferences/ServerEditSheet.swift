import SwiftUI

struct ServerEditSheet: View {
    let context: ServerEditSheetContext
    let onSave: (Node) -> Void
    let onCancel: () -> Void

    @State private var displayName: String
    @State private var kind: NodeKind
    @State private var sshUser: String
    @State private var sshHost: String
    @State private var tagsString: String
    @State private var enabled: Bool
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
            _existingId = State(initialValue: nil)
        case .edit(let node):
            _displayName = State(initialValue: node.displayName)
            _kind = State(initialValue: node.kind)
            _sshUser = State(initialValue: node.sshUser ?? "")
            _sshHost = State(initialValue: node.sshHost ?? "")
            _tagsString = State(initialValue: node.tags.joined(separator: ", "))
            _enabled = State(initialValue: node.enabled)
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
        .frame(width: 440)
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
            enabled: enabled
        )
        onSave(node)
    }
}
