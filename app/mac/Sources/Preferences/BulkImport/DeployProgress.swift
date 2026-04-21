import SwiftUI

/// Step 4 — shows live per-row progress as each selected row goes through
/// bootstrap-and-verify. Successful rows are committed to NodeStore the
/// moment they succeed (not end-of-batch) so partial progress is kept if
/// the user closes the sheet mid-run.
struct DeployProgress: View {
    @Binding var rows: [ImportRow]
    @Binding var keepFailedAsDisabled: Bool
    let nodeStore: NodeStore

    @State private var running: Bool = false
    @State private var started: Bool = false
    @State private var committedIDs: Set<UUID> = []

    /// Deploy queue — filters to the rows included at the moment the
    /// wizard transitioned into this step. We snapshot into this array
    /// (rather than re-filtering `rows` each time) so the user ticking
    /// "Keep failed as disabled" or editing upstream state doesn't shift
    /// what we're working on mid-run.
    @State private var queue: [UUID] = []

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Deploying")
                    .font(.headline)
                Spacer()
                if running {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text(statusLine).font(.callout).foregroundStyle(.secondary)
                    }
                } else if started {
                    Text(statusLine).font(.callout).foregroundStyle(.secondary)
                }
            }

            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(deployRows) { row in
                        deployRow(row)
                        Divider().opacity(0.4)
                    }
                }
            }
            .frame(minHeight: 240, maxHeight: .infinity)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(Color(nsColor: .textBackgroundColor))
            )
        }
        .padding(20)
        .task {
            if !started {
                started = true
                await runDeploy()
            }
        }
        .onChange(of: keepFailedAsDisabled) { _, newValue in
            // If the user changes this after deploy has finished, retro-apply
            // it to failed rows that haven't been committed yet. We never
            // remove a node once committed (would be destructive).
            if !running && newValue { commitFailedAsDisabled() }
        }
    }

    private var deployRows: [ImportRow] {
        rows.filter { queue.contains($0.id) }
    }

    private var statusLine: String {
        let total = queue.count
        let okCount = rows.filter { queue.contains($0.id) && isOK($0) }.count
        let failCount = rows.filter { queue.contains($0.id) && isFailed($0) }.count
        let pending = total - okCount - failCount
        if running {
            return "\(okCount) OK · \(failCount) failed · \(pending) remaining"
        }
        if failCount == 0 {
            return "All \(total) deployed."
        }
        return "\(okCount) of \(total) deployed · \(failCount) failed."
    }

    @ViewBuilder
    private func deployRow(_ row: ImportRow) -> some View {
        HStack(spacing: 10) {
            statusIcon(row)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.displayName)
                    .font(.body)
                Text(row.sshUser.isEmpty ? row.sshHost : "\(row.sshUser)@\(row.sshHost)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(.secondary)
            }
            Spacer()
            statusText(row)
            if isFailed(row) {
                Button("Retry") {
                    Task { await deployOne(id: row.id) }
                }
                .disabled(running)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private func statusIcon(_ row: ImportRow) -> some View {
        switch row.status {
        case .ok:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed:
            Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
        case .testing, .deploying:
            ProgressView().controlSize(.small)
        case .pending, .duplicate:
            Circle().fill(Color.secondary.opacity(0.4)).frame(width: 10, height: 10)
        }
    }

    @ViewBuilder
    private func statusText(_ row: ImportRow) -> some View {
        switch row.status {
        case .pending:
            Text("pending").font(.caption).foregroundStyle(.secondary)
        case .testing:
            Text("testing…").font(.caption).foregroundStyle(.secondary)
        case .deploying(let stage):
            Text(stage).font(.caption).foregroundStyle(.secondary)
        case .ok(let detail):
            Text(detail).font(.caption).foregroundStyle(.green)
        case .failed(let msg):
            Text(msg).font(.caption).foregroundStyle(.red).lineLimit(2)
                .help(msg)
        case .duplicate:
            Text("skipped (already added)").font(.caption).foregroundStyle(.secondary)
        }
    }

    // MARK: - Deploy

    @MainActor
    private func runDeploy() async {
        let initialIDs = rows.filter { $0.included && $0.isValid }.map { $0.id }
        queue = initialIDs
        running = true
        defer {
            running = false
            commitFailedAsDisabled()
        }
        let batchSize = 4
        var i = 0
        while i < initialIDs.count {
            let slice = Array(initialIDs[i..<min(i + batchSize, initialIDs.count)])
            await withTaskGroup(of: Void.self) { group in
                for id in slice {
                    group.addTask { @MainActor in
                        await deployOne(id: id)
                    }
                }
            }
            i += batchSize
        }
    }

    @MainActor
    private func deployOne(id: UUID) async {
        guard let idx = rows.firstIndex(where: { $0.id == id }) else { return }
        rows[idx].status = .deploying("connecting…")
        let node = rows[idx].toNode()

        // Fast-path for local nodes: no bootstrap, just commit.
        if node.kind == .local {
            commit(id: id, enabled: true)
            if let i = rows.firstIndex(where: { $0.id == id }) {
                rows[i].status = .ok("local")
            }
            return
        }

        do {
            if let i = rows.firstIndex(where: { $0.id == id }) {
                rows[i].status = .deploying("copying sampler…")
            }
            let report = try await SSHBootstrap.bootstrapAndVerify(node: node)
            if let i = rows.firstIndex(where: { $0.id == id }) {
                rows[i].status = .ok("deployed (\(report.triple))")
            }
            commit(id: id, enabled: true)
        } catch {
            let msg = (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
            if let i = rows.firstIndex(where: { $0.id == id }) {
                rows[i].status = .failed(msg)
            }
        }
    }

    /// Append the row's node to NodeStore exactly once, even if the user
    /// hits Retry several times or we end up re-queuing.
    @MainActor
    private func commit(id: UUID, enabled: Bool) {
        guard !committedIDs.contains(id),
              let row = rows.first(where: { $0.id == id }) else { return }
        let node = enabled ? row.toNode() : row.toDisabledNode()
        nodeStore.add(node)
        committedIDs.insert(id)
    }

    /// After deploy finishes, write out any failed rows as disabled nodes
    /// if the user opted in. We keep the sheet behavior idempotent: if
    /// the user retries a failed row and it succeeds later, that same id
    /// won't be re-added because of `committedIDs`.
    @MainActor
    private func commitFailedAsDisabled() {
        guard keepFailedAsDisabled else { return }
        for row in rows {
            guard queue.contains(row.id), isFailed(row), !committedIDs.contains(row.id) else { continue }
            let node = row.toDisabledNode()
            nodeStore.add(node)
            committedIDs.insert(row.id)
        }
    }

    // MARK: - Status helpers

    private func isOK(_ row: ImportRow) -> Bool {
        if case .ok = row.status { return true }
        return false
    }

    private func isFailed(_ row: ImportRow) -> Bool {
        if case .failed = row.status { return true }
        return false
    }
}
