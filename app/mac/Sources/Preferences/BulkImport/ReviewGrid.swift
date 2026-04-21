import SwiftUI

/// Step 3 — editable grid of the rows produced by whichever step-2 source
/// ran. The user can uncheck, rename, change SSH user/kind, remove rows,
/// and run a per-row Test before committing to Deploy.
struct ReviewGrid: View {
    @Binding var rows: [ImportRow]
    let nodeStore: NodeStore

    @State private var bulkUser: String = NSUserName()
    @State private var applyScope: ApplyScope = .emptyOnly
    @State private var testingAll: Bool = false

    enum ApplyScope: String, CaseIterable, Identifiable {
        case emptyOnly
        case all

        var id: String { rawValue }
        var label: String {
            switch self {
            case .emptyOnly: return "blanks only"
            case .all: return "all rows"
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            topControls
            Divider()
            table
            footer
        }
        .padding(20)
    }

    // MARK: - Top controls

    private var topControls: some View {
        HStack(spacing: 10) {
            Button {
                let allOn = rows.contains(where: { !$0.included })
                for i in rows.indices { rows[i].included = allOn }
            } label: {
                Text(rows.contains(where: { !$0.included }) ? "Select all" : "Deselect all")
            }

            Divider().frame(height: 18)

            Text("Default SSH user:")
                .font(.callout)
            TextField("", text: $bulkUser)
                .frame(width: 140)
            Picker("", selection: $applyScope) {
                ForEach(ApplyScope.allCases) { s in
                    Text(s.label).tag(s)
                }
            }
            .labelsHidden()
            .frame(width: 130)
            Button("Apply to all") { applyBulkUser() }

            Spacer()

            Button {
                Task { await testAllSelected() }
            } label: {
                if testingAll {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text("Testing…")
                    }
                } else {
                    Text("Test all selected")
                }
            }
            .disabled(testingAll || selectedSSHCount == 0)
        }
    }

    // MARK: - Table

    private var table: some View {
        VStack(spacing: 0) {
            headerRow
            Divider()
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach($rows) { rowBinding in
                        rowView(rowBinding: rowBinding)
                        Divider().opacity(0.4)
                    }
                    if rows.isEmpty {
                        Text("No rows.")
                            .foregroundStyle(.secondary)
                            .padding(20)
                    }
                }
            }
        }
        .frame(minHeight: 200, maxHeight: .infinity)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(Color(nsColor: .textBackgroundColor))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(Color.secondary.opacity(0.2), lineWidth: 1)
        )
    }

    private var headerRow: some View {
        HStack(spacing: 8) {
            Text("").frame(width: 24) // checkbox column
            Text("Name").frame(width: 140, alignment: .leading)
            Text("Host / IP").frame(width: 160, alignment: .leading)
            Text("SSH user").frame(width: 110, alignment: .leading)
            Text("Kind").frame(width: 70, alignment: .leading)
            Text("Tags").frame(maxWidth: .infinity, alignment: .leading)
            Text("Status").frame(width: 160, alignment: .leading)
            Text("").frame(width: 60) // test + remove
        }
        .font(.caption).bold()
        .foregroundStyle(.secondary)
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private func rowView(rowBinding: Binding<ImportRow>) -> some View {
        let row = rowBinding.wrappedValue
        HStack(spacing: 8) {
            Toggle("", isOn: rowBinding.included)
                .labelsHidden()
                .frame(width: 24)

            TextField("", text: rowBinding.displayName)
                .frame(width: 140)
                .textFieldStyle(.roundedBorder)

            TextField("", text: rowBinding.sshHost)
                .frame(width: 160)
                .font(.system(.body, design: .monospaced))
                .textFieldStyle(.roundedBorder)

            TextField("", text: rowBinding.sshUser)
                .frame(width: 110)
                .font(.system(.body, design: .monospaced))
                .textFieldStyle(.roundedBorder)
                .disabled(row.kind == .local)

            Picker("", selection: rowBinding.kind) {
                Text("SSH").tag(NodeKind.ssh)
                Text("Local").tag(NodeKind.local)
            }
            .labelsHidden()
            .frame(width: 70)

            TextField("", text: Binding(
                get: { rowBinding.wrappedValue.tags.joined(separator: ", ") },
                set: { newValue in
                    rowBinding.wrappedValue.tags = newValue
                        .split(separator: ",")
                        .map { $0.trimmingCharacters(in: .whitespaces) }
                        .filter { !$0.isEmpty }
                }
            ))
            .frame(maxWidth: .infinity)
            .textFieldStyle(.roundedBorder)

            statusCell(row)
                .frame(width: 160, alignment: .leading)

            HStack(spacing: 4) {
                Button {
                    let id = row.id
                    Task { await testRow(id: id) }
                } label: {
                    Image(systemName: "bolt.circle")
                }
                .buttonStyle(.borderless)
                .disabled(row.kind == .local
                          || !row.isValid
                          || isTestingRow(row))

                Button {
                    let id = row.id
                    rows.removeAll { $0.id == id }
                } label: {
                    Image(systemName: "xmark")
                }
                .buttonStyle(.borderless)
            }
            .frame(width: 60)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .opacity(row.included ? 1.0 : 0.55)
    }

    @ViewBuilder
    private func statusCell(_ row: ImportRow) -> some View {
        switch row.status {
        case .pending:
            if !row.isValid {
                badge(text: row.kind == .ssh ? "Missing SSH user/host" : "Missing name",
                      color: .orange)
            } else if row.isDuplicate {
                badge(text: "Already added", color: .yellow)
            } else {
                Text("—").foregroundStyle(.secondary).font(.caption)
            }
        case .testing:
            HStack(spacing: 4) {
                ProgressView().controlSize(.small)
                Text("testing…").font(.caption).foregroundStyle(.secondary)
            }
        case .deploying(let stage):
            HStack(spacing: 4) {
                ProgressView().controlSize(.small)
                Text(stage).font(.caption).foregroundStyle(.secondary)
            }
        case .ok(let detail):
            HStack(spacing: 4) {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                Text(detail).font(.caption).lineLimit(1)
            }
        case .failed(let msg):
            HStack(spacing: 4) {
                Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
                Text(msg).font(.caption).lineLimit(1)
            }
            .help(msg)
        case .duplicate:
            badge(text: "Already added", color: .yellow)
        }
    }

    private func badge(text: String, color: Color) -> some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(color)
    }

    // MARK: - Footer

    private var footer: some View {
        HStack {
            Text("\(selectedCount) of \(rows.count) selected")
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(.top, 4)
    }

    // MARK: - Actions

    private var selectedCount: Int {
        rows.filter { $0.included }.count
    }

    private var selectedSSHCount: Int {
        rows.filter { $0.included && $0.kind == .ssh && $0.isValid }.count
    }

    private func isTestingRow(_ row: ImportRow) -> Bool {
        if case .testing = row.status { return true }
        return false
    }

    private func applyBulkUser() {
        let trimmed = bulkUser.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return }
        for i in rows.indices {
            switch applyScope {
            case .emptyOnly:
                if rows[i].sshUser.trimmingCharacters(in: .whitespaces).isEmpty {
                    rows[i].sshUser = trimmed
                }
            case .all:
                rows[i].sshUser = trimmed
            }
        }
    }

    @MainActor
    private func testRow(id: UUID) async {
        guard let idx = rows.firstIndex(where: { $0.id == id }) else { return }
        rows[idx].status = .testing
        let node = rows[idx].toNode()
        do {
            let report = try await SSHBootstrap.bootstrapAndVerify(node: node)
            if let idx2 = rows.firstIndex(where: { $0.id == id }) {
                rows[idx2].status = .ok("reachable (\(report.triple))")
            }
        } catch {
            let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
            if let idx2 = rows.firstIndex(where: { $0.id == id }) {
                rows[idx2].status = .failed(msg)
            }
        }
    }

    /// Parallel Test across all selected SSH rows with a cap of 4 so we
    /// don't saturate the user's network or bombard a jump host.
    @MainActor
    private func testAllSelected() async {
        testingAll = true
        defer { testingAll = false }
        let ids = rows
            .filter { $0.included && $0.kind == .ssh && $0.isValid }
            .map { $0.id }
        for i in rows.indices where ids.contains(rows[i].id) {
            rows[i].status = .testing
        }
        // Index-sliced batches; runs 4 at a time. Simpler than a
        // semaphore + task group and the concurrency cap is all we need.
        let batchSize = 4
        var i = 0
        while i < ids.count {
            let slice = Array(ids[i..<min(i + batchSize, ids.count)])
            await withTaskGroup(of: Void.self) { group in
                for id in slice {
                    group.addTask { @MainActor in
                        await testRow(id: id)
                    }
                }
            }
            i += batchSize
        }
    }
}
