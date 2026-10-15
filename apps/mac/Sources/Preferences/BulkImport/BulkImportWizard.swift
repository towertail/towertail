import SwiftUI
import Foundation

enum WizardStep: Hashable {
    case source
    case fetch
    case review
    case deploy
}

enum WizardSource: Hashable {
    case tailscale
    case csv
    case paste
}

/// One candidate node as it moves through the wizard. Mutable so the
/// review grid can flip `included`, edit SSH user per row, etc.
struct ImportRow: Identifiable, Equatable {
    enum Status: Equatable {
        case pending
        case testing
        case deploying(String) // stage label ("copying", "verifying", …)
        case ok(String)        // short success detail (triple, etc.)
        case failed(String)    // error message
        case duplicate         // matches an existing node
    }

    let id: UUID
    var displayName: String
    var sshHost: String
    var sshUser: String
    var kind: NodeKind
    var authMethod: AuthMethod
    /// Plaintext in-memory only; written to Keychain on deploy. Never touches disk.
    var password: String
    var tags: [String]
    var included: Bool
    var status: Status
    /// True when the row matched an existing node at review time. The UI
    /// uses this to show a yellow badge even after the user force-toggles
    /// `included` back on.
    var isDuplicate: Bool

    init(
        id: UUID = UUID(),
        displayName: String,
        sshHost: String,
        sshUser: String = "",
        kind: NodeKind = .ssh,
        authMethod: AuthMethod = .key,
        password: String = "",
        tags: [String] = [],
        included: Bool = true,
        status: Status = .pending,
        isDuplicate: Bool = false
    ) {
        self.id = id
        self.displayName = displayName
        self.sshHost = sshHost
        self.sshUser = sshUser
        self.kind = kind
        self.authMethod = authMethod
        self.password = password
        self.tags = tags
        self.included = included
        self.status = status
        self.isDuplicate = isDuplicate
    }

    /// True when the row has what it needs to attempt an SSH bootstrap.
    /// Local rows only need a name.
    var isValid: Bool {
        guard !displayName.trimmingCharacters(in: .whitespaces).isEmpty else { return false }
        switch kind {
        case .local:
            return true
        case .ssh:
            return !sshHost.trimmingCharacters(in: .whitespaces).isEmpty
                && !sshUser.trimmingCharacters(in: .whitespaces).isEmpty
        }
    }

    /// Convert to a persistent Node. Called only at deploy-time after the
    /// row has been verified, so we can trust the fields.
    func toNode() -> Node {
        Node(
            id: id,
            displayName: displayName.trimmingCharacters(in: .whitespaces),
            kind: kind,
            sshUser: kind == .ssh ? sshUser.trimmingCharacters(in: .whitespaces) : nil,
            sshHost: kind == .ssh ? sshHost.trimmingCharacters(in: .whitespaces) : nil,
            authMethod: kind == .ssh ? authMethod : .key,
            tags: tags,
            enabled: true
        )
    }

    /// Same as `toNode` but forces `enabled = false`. Used for failed rows
    /// when the "Keep failed as disabled" toggle is on, so the user can
    /// fix SSH config and re-test without re-importing.
    func toDisabledNode() -> Node {
        var n = toNode()
        n.enabled = false
        return n
    }
}

struct BulkImportWizard: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(NodeStore.self) private var nodeStore

    @State private var step: WizardStep = .source
    @State private var source: WizardSource? = nil
    @State private var rows: [ImportRow] = []
    @State private var fetchError: String? = nil
    @State private var keepFailedAsDisabled: Bool = true

    /// Persisted across invocations so a user who prefers MagicDNS keeps
    /// that preference between imports.
    @AppStorage("bulkImport.preferMagicDNS") private var preferMagicDNS: Bool = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            content
                .frame(minWidth: 960, minHeight: 440)
            Divider()
            footer
        }
        .frame(width: 1040, height: 620)
    }

    // MARK: - Header

    private var header: some View {
        HStack(spacing: 14) {
            stepLabel("Source", .source, index: 1)
            stepDivider()
            stepLabel(fetchStepTitle, .fetch, index: 2)
            stepDivider()
            stepLabel("Review", .review, index: 3)
            stepDivider()
            stepLabel("Deploy", .deploy, index: 4)
            Spacer()
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }

    private var fetchStepTitle: String {
        switch source {
        case .tailscale: return "Tailscale"
        case .csv: return "CSV / TSV"
        case .paste: return "Paste"
        case nil: return "Fetch"
        }
    }

    @ViewBuilder
    private func stepLabel(_ title: String, _ target: WizardStep, index: Int) -> some View {
        let active = step == target
        HStack(spacing: 6) {
            ZStack {
                Circle()
                    .fill(active ? Color.accentColor : Color.secondary.opacity(0.25))
                    .frame(width: 20, height: 20)
                Text("\(index)")
                    .font(.caption).bold()
                    .foregroundStyle(active ? .white : .secondary)
            }
            Text(title)
                .font(.subheadline)
                .foregroundStyle(active ? .primary : .secondary)
        }
    }

    private func stepDivider() -> some View {
        Rectangle()
            .fill(Color.secondary.opacity(0.3))
            .frame(width: 24, height: 1)
    }

    // MARK: - Content

    @ViewBuilder
    private var content: some View {
        switch step {
        case .source:
            SourcePicker(selection: $source) { chosen in
                source = chosen
                fetchError = nil
                rows = []
                step = .fetch
            }
        case .fetch:
            fetchView
        case .review:
            ReviewGrid(rows: $rows, nodeStore: nodeStore)
        case .deploy:
            DeployProgress(
                rows: $rows,
                keepFailedAsDisabled: $keepFailedAsDisabled,
                nodeStore: nodeStore
            )
        }
    }

    @ViewBuilder
    private var fetchView: some View {
        switch source {
        case .tailscale:
            TailscaleSource(
                rows: $rows,
                error: $fetchError,
                preferMagicDNS: $preferMagicDNS
            )
        case .csv:
            CSVSource(rows: $rows, error: $fetchError)
        case .paste:
            PasteSource(rows: $rows)
        case nil:
            Text("Choose a source")
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: - Footer

    private var footer: some View {
        HStack {
            if step == .deploy {
                Toggle("Keep failed as disabled", isOn: $keepFailedAsDisabled)
                    .help("Adds failed rows to Servers with Enabled off, so you can fix SSH config and re-test from the list.")
                    .font(.callout)
            }
            Spacer()
            Button("Cancel") { dismiss() }

            if step != .source {
                Button("Back") { goBack() }
            }

            switch step {
            case .source:
                EmptyView()
            case .fetch:
                Button("Next") {
                    markDuplicates()
                    step = .review
                }
                .keyboardShortcut(.defaultAction)
                .disabled(rows.isEmpty)
            case .review:
                Button("Deploy (\(selectedCount))") {
                    // Reset status for rows that had an ad-hoc Test before
                    // deploy, so the progress list starts from pending.
                    for i in rows.indices where rows[i].included {
                        if case .failed = rows[i].status { continue }
                        rows[i].status = .pending
                    }
                    step = .deploy
                }
                .keyboardShortcut(.defaultAction)
                .disabled(selectedCount == 0)
            case .deploy:
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }

    private var selectedCount: Int {
        rows.filter { $0.included && $0.isValid }.count
    }

    private func goBack() {
        switch step {
        case .source: break
        case .fetch:
            step = .source
            source = nil
            rows = []
        case .review:
            step = .fetch
        case .deploy:
            step = .review
        }
    }

    /// Flag rows that match an existing node, and uncheck them by default.
    /// Match on sshHost (case-insensitive trimmed) then displayName.
    private func markDuplicates() {
        let existing = nodeStore.nodes
        for i in rows.indices {
            let host = rows[i].sshHost.trimmingCharacters(in: .whitespaces).lowercased()
            let name = rows[i].displayName.trimmingCharacters(in: .whitespaces).lowercased()
            let dup = existing.contains { n in
                let nHost = (n.sshHost ?? "").trimmingCharacters(in: .whitespaces).lowercased()
                if !host.isEmpty && nHost == host { return true }
                return n.displayName.lowercased() == name && !name.isEmpty
            }
            if dup {
                rows[i].isDuplicate = true
                rows[i].included = false
                rows[i].status = .duplicate
            }
        }
    }
}
