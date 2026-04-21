import SwiftUI

struct ServersPane: View {
    @Environment(NodeStore.self) private var nodeStore
    @Environment(ServerStore.self) private var serverStore

    @State private var selection: Set<Node.ID> = []
    @State private var sheet: ServerEditSheetContext?
    @State private var testResult: TestResult?
    @State private var bulkRunning: Bool = false

    private struct TestResult: Identifiable {
        let id = UUID()
        let ok: Bool
        let message: String
    }

    /// Nodes currently selected in the table, in table order so bulk
    /// operations report progress from top to bottom.
    private var selectedNodes: [Node] {
        nodeStore.nodes.filter { selection.contains($0.id) }
    }

    private var primarySelection: Node? {
        selectedNodes.first
    }

    private var hasSSHSelection: Bool {
        selectedNodes.contains(where: { $0.kind == .ssh })
    }

    private var sshSelectionCount: Int {
        selectedNodes.filter { $0.kind == .ssh }.count
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Table(nodeStore.nodes, selection: $selection) {
                TableColumn("Name") { n in
                    HStack {
                        Circle()
                            .fill(statusColor(for: n))
                            .frame(width: 8, height: 8)
                        Text(n.displayName)
                    }
                }
                TableColumn("Kind") { n in
                    Text(n.kind == .local ? "Local" : "SSH")
                }
                TableColumn("User@Host") { n in
                    Text(n.userAtHost)
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(.secondary)
                }
                TableColumn("Status") { n in
                    Text(statusLabel(for: n))
                        .foregroundStyle(.secondary)
                }
                TableColumn("Enabled") { n in
                    Toggle("", isOn: Binding(
                        get: { n.enabled },
                        set: { nodeStore.setEnabled(id: n.id, enabled: $0) }
                    ))
                    .labelsHidden()
                }
            }
            .frame(minHeight: 200)

            HStack(spacing: 8) {
                Button {
                    sheet = .new
                } label: {
                    Image(systemName: "plus")
                }
                .disabled(bulkRunning)

                Button {
                    for id in selection {
                        nodeStore.remove(id: id)
                    }
                    selection = []
                } label: {
                    Image(systemName: "minus")
                }
                .disabled(selection.isEmpty || bulkRunning)

                Button {
                    if let node = primarySelection {
                        sheet = .edit(node)
                    }
                } label: {
                    Text("Edit")
                }
                // Editing is per-node — greyed out (but not hidden) when
                // the user has a multi-select so the affordance is stable.
                .disabled(selection.count != 1 || bulkRunning)

                Button {
                    runBulkTest()
                } label: {
                    Text(selection.count > 1 ? "Test (\(selection.count))" : "Test")
                }
                .disabled(selection.isEmpty || bulkRunning)

                Button {
                    runBulkReinstall()
                } label: {
                    Text(sshSelectionCount > 1 ? "Reinstall agent (\(sshSelectionCount))" : "Reinstall agent")
                }
                .disabled(!hasSSHSelection || bulkRunning)
                .help("Force-upload the bundled agent binary to ~/.towertail/towertail-agent on every selected SSH host. Use this after upgrading the app when remote agents are out of date.")

                if bulkRunning {
                    ProgressView().controlSize(.small)
                }

                Spacer()
            }

            if let r = testResult {
                HStack(spacing: 6) {
                    Image(systemName: r.ok ? "checkmark.circle.fill" : "xmark.octagon.fill")
                        .foregroundStyle(r.ok ? .green : .red)
                    Text(r.message)
                        .font(.callout)
                }
                .transition(.opacity)
            }

            Divider()
            Text("SSH nodes require the agent at `~/.towertail/towertail-agent` on the remote host. **Test** runs a sample end-to-end (uploading the bundled binary if missing). **Reinstall agent** force-pushes the binary — use this after upgrading Towertail if the remote agent is out of date. Key-based auth only; add the relevant key to `~/.ssh/config` or an ssh-agent.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .sheet(item: $sheet) { ctx in
            ServerEditSheet(context: ctx) { saved in
                switch ctx {
                case .new: nodeStore.add(saved)
                case .edit: nodeStore.update(saved)
                }
                sheet = nil
            } onCancel: {
                sheet = nil
            }
            .environment(nodeStore)
        }
    }

    private func statusColor(for n: Node) -> Color {
        guard let vm = serverStore.serverVMs.first(where: { $0.id == n.id }) else {
            return .secondary.opacity(0.5)
        }
        return vm.statusDotColor
    }

    private func statusLabel(for n: Node) -> String {
        guard let vm = serverStore.serverVMs.first(where: { $0.id == n.id }) else {
            return n.enabled ? "—" : "disabled"
        }
        switch vm.state {
        case .unknown: return "—"
        case .online: return "online"
        case .warn: return "warn"
        case .critical: return "critical"
        case .offline(let reason): return "offline: \(reason)"
        }
    }

    /// Force-uploads the bundled agent to the remote host and runs a sanity
    /// sample. Distinct from Test so users have an unambiguous way to push
    /// a new binary (e.g. after upgrading the Mac app) without reading
    /// source to realize Test already does this as a side effect.
    private func reinstallAgent(node: Node) {
        guard node.kind == .ssh else { return }
        testResult = TestResult(ok: true, message: "Reinstalling agent on \(node.displayName)…")
        Task {
            do {
                let report = try await SSHBootstrap.bootstrapAndVerify(node: node)
                let procs = report.sample.procs
                let procsInfo: String
                if let procs {
                    procsInfo = " · procs: \(procs.visible) (\(procs.root ? "root" : "user"))"
                } else {
                    procsInfo = " · procs: none"
                }
                await MainActor.run {
                    testResult = TestResult(
                        ok: true,
                        message: "Reinstalled (\(report.triple)) → \(report.remotePath)\(procsInfo)"
                    )
                }
            } catch {
                let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
                await MainActor.run {
                    testResult = TestResult(ok: false, message: "Reinstall failed: \(msg)")
                }
            }
        }
    }

    private func runTest(node: Node) {
        testResult = TestResult(ok: true, message: "Testing \(node.displayName)…")
        Task {
            do {
                if node.kind == .ssh {
                    let report = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    let s = report.sample
                    await MainActor.run {
                        testResult = TestResult(
                            ok: true,
                            message: "OK (\(report.triple)): \(s.host.name) · cpu \(Int(round(s.cpu.pct)))% · cores \(s.cpu.cores)"
                        )
                    }
                } else {
                    let invoker = makeInvoker(for: node)
                    let sample = try await invoker.invokeOnce(node: node)
                    await MainActor.run {
                        testResult = TestResult(
                            ok: true,
                            message: "OK: \(sample.host.name) · cpu \(Int(round(sample.cpu.pct)))% · cores \(sample.cpu.cores)"
                        )
                    }
                }
            } catch {
                let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
                await MainActor.run {
                    testResult = TestResult(ok: false, message: msg)
                }
            }
        }
    }

    /// Runs Test across every selected node. For a single selection this
    /// behaves exactly like the old single-node path — we delegate to
    /// `runTest` so the OK/fail message still names the host. For a bulk
    /// selection we roll results up into one summary line with per-failure
    /// detail so the user sees which specific hosts failed.
    private func runBulkTest() {
        let nodes = selectedNodes
        guard !nodes.isEmpty else { return }
        if nodes.count == 1 {
            runTest(node: nodes[0])
            return
        }
        bulkRunning = true
        testResult = TestResult(ok: true, message: "Testing \(nodes.count) hosts…")
        Task {
            var okCount = 0
            var failures: [(String, String)] = []
            for node in nodes {
                do {
                    if node.kind == .ssh {
                        _ = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    } else {
                        _ = try await makeInvoker(for: node).invokeOnce(node: node)
                    }
                    okCount += 1
                } catch {
                    let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
                    failures.append((node.displayName, msg))
                }
            }
            await MainActor.run {
                bulkRunning = false
                if failures.isEmpty {
                    testResult = TestResult(ok: true, message: "Tested \(okCount)/\(nodes.count) hosts OK")
                } else {
                    let head = "Tested \(okCount)/\(nodes.count) OK · \(failures.count) failed:"
                    let detail = failures.prefix(3)
                        .map { "\($0.0): \($0.1)" }
                        .joined(separator: " · ")
                    let tail = failures.count > 3 ? " · +\(failures.count - 3) more" : ""
                    testResult = TestResult(ok: false, message: "\(head) \(detail)\(tail)")
                }
            }
        }
    }

    /// Force-reinstalls the agent on every selected SSH node. Local nodes
    /// in the selection are skipped silently — their binary is always
    /// served from the bundled Resources path.
    private func runBulkReinstall() {
        let nodes = selectedNodes.filter { $0.kind == .ssh }
        guard !nodes.isEmpty else { return }
        if nodes.count == 1 {
            reinstallAgent(node: nodes[0])
            return
        }
        bulkRunning = true
        testResult = TestResult(ok: true, message: "Reinstalling agent on \(nodes.count) hosts…")
        Task {
            var okCount = 0
            var failures: [(String, String)] = []
            for node in nodes {
                do {
                    _ = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    okCount += 1
                } catch {
                    let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
                    failures.append((node.displayName, msg))
                }
            }
            await MainActor.run {
                bulkRunning = false
                if failures.isEmpty {
                    testResult = TestResult(ok: true, message: "Reinstalled agent on \(okCount)/\(nodes.count) hosts")
                } else {
                    let head = "Reinstalled \(okCount)/\(nodes.count) · \(failures.count) failed:"
                    let detail = failures.prefix(3)
                        .map { "\($0.0): \($0.1)" }
                        .joined(separator: " · ")
                    let tail = failures.count > 3 ? " · +\(failures.count - 3) more" : ""
                    testResult = TestResult(ok: false, message: "\(head) \(detail)\(tail)")
                }
            }
        }
    }
}

enum ServerEditSheetContext: Identifiable {
    case new
    case edit(Node)

    var id: String {
        switch self {
        case .new: return "new"
        case .edit(let n): return n.id.uuidString
        }
    }
}
