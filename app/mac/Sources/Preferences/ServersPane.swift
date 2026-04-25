import SwiftUI

struct ServersPane: View {
    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend

    @State private var selection: Set<Node.ID> = []
    @State private var sheet: ServerEditSheetContext?
    @State private var testResult: TestResult?
    @State private var bulkRunning: Bool = false
    @State private var showBulkImport: Bool = false

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
            ServersTable(selection: $selection)
                .frame(minHeight: 200)

            HStack(spacing: 8) {
                Button {
                    sheet = .new
                } label: {
                    Image(systemName: "plus")
                }
                .disabled(bulkRunning)

                Button {
                    showBulkImport = true
                } label: {
                    Label("Bulk import…", systemImage: "square.and.arrow.down.on.square")
                }
                .disabled(bulkRunning)
                .help("Import servers from Tailscale, a CSV file, or a pasted host list.")

                Button {
                    let ids = selection
                    let b = backend
                    Task {
                        for id in ids {
                            try? await b?.removeNode(id: id)
                        }
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
                    Text(sshSelectionCount > 1 ? "Reinstall sampler (\(sshSelectionCount))" : "Reinstall sampler")
                }
                .disabled(!hasSSHSelection || bulkRunning)
                .help("Force-upload the bundled sampler binary to ~/.towertail/towertail-sampler on every selected SSH host. Use this after upgrading the app when remote samplers are out of date.")

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

            // Scoped to its own view so its ServerSettings observation
            // subgraph doesn't share a parent with the Table above. Placing
            // a Toggle that reads ServerSettings in the same body as a Table
            // triggers an AGGraphGetAttributeSubgraph precondition crash
            // when the preferences window tabs switch (macOS 15 /
            // SwiftUI 6 bug).
            AutoUpdateToggleRow()

            Text("SSH nodes require the sampler at `~/.towertail/towertail-sampler` on the remote host. **Test** runs a sample end-to-end (uploading the bundled binary if missing). **Reinstall sampler** force-pushes the binary — use this after upgrading Towertail if the remote sampler is out of date. Key-based auth only; add the relevant key to `~/.ssh/config` or an ssh-sampler.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .sheet(item: $sheet) { ctx in
            ServerEditSheet(context: ctx) { saved in
                let b = backend
                Task {
                    switch ctx {
                    case .new: try? await b?.addNode(saved)
                    case .edit: try? await b?.updateNode(saved)
                    }
                }
                sheet = nil
            } onCancel: {
                sheet = nil
            }
        }
        .sheet(isPresented: $showBulkImport) {
            BulkImportWizard()
        }
    }

    /// Force-uploads the bundled sampler to the remote host and runs a sanity
    /// sample. Distinct from Test so users have an unambiguous way to push
    /// a new binary (e.g. after upgrading the Mac app) without reading
    /// source to realize Test already does this as a side effect.
    private func reinstallSampler(node: Node) {
        guard node.kind == .ssh else { return }
        testResult = TestResult(ok: true, message: "Reinstalling sampler on \(node.displayName)…")
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
                let msg = (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
                await MainActor.run {
                    testResult = TestResult(ok: false, message: "Reinstall failed: \(msg)")
                }
            }
        }
    }

    private func runTest(node: Node) {
        testResult = TestResult(ok: true, message: "Testing \(node.displayName)…")
        let backend = self.backend
        let nodeStore = self.nodeStore
        Task {
            do {
                let sampleHost: HostInfo
                let cpuPct: Double
                let cores: Int
                let okMessage: String
                if node.kind == .ssh {
                    let report = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    let s = report.sample
                    sampleHost = s.host
                    cpuPct = s.cpu.pct
                    cores = s.cpu.cores
                    okMessage = "OK (\(report.triple)): \(s.host.name) · cpu \(Int(round(cpuPct)))% · cores \(cores)"
                } else {
                    let invoker = makeInvoker(for: node)
                    let sample = try await invoker.invokeOnce(node: node)
                    sampleHost = sample.host
                    cpuPct = sample.cpu.pct
                    cores = sample.cpu.cores
                    okMessage = "OK: \(sampleHost.name) · cpu \(Int(round(cpuPct)))% · cores \(cores)"
                }
                await MainActor.run {
                    testResult = TestResult(ok: true, message: okMessage)
                    // Mark the node as having connected so the warm-state
                    // flag is set even if the supervisor's pacer was the
                    // one that observed the failure path. No-op on
                    // already-warm nodes.
                    nodeStore.markConnected(id: node.id)
                }
                // Kick the supervisor so a pacer that halted on a
                // permanent error (e.g. "all SSH keys rejected" before
                // the user copied the key over) drops its parked entry
                // and respawns. Without this, polling stays halted even
                // though Test just succeeded — the user has to disable
                // and re-enable the node to get back to live.
                await backend?.respawnPacer(id: node.id)
            } catch {
                let msg = (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
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
        let backend = self.backend
        let nodeStore = self.nodeStore
        Task {
            var okCount = 0
            var failures: [(String, String)] = []
            var succeededIDs: [UUID] = []
            for node in nodes {
                do {
                    if node.kind == .ssh {
                        _ = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    } else {
                        _ = try await makeInvoker(for: node).invokeOnce(node: node)
                    }
                    okCount += 1
                    succeededIDs.append(node.id)
                } catch {
                    let msg = (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
                    failures.append((node.displayName, msg))
                }
            }
            // Stamp warm flag + nudge any halted pacers back to life for
            // every host that just verified. Mirrors the single-host path
            // so a bulk test is just as effective at recovering halted
            // hosts as Test on each one individually.
            await MainActor.run {
                for id in succeededIDs { nodeStore.markConnected(id: id) }
            }
            for id in succeededIDs {
                await backend?.respawnPacer(id: id)
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

    /// Force-reinstalls the sampler on every selected SSH node. Local nodes
    /// in the selection are skipped silently — their binary is always
    /// served from the bundled Resources path.
    private func runBulkReinstall() {
        let nodes = selectedNodes.filter { $0.kind == .ssh }
        guard !nodes.isEmpty else { return }
        if nodes.count == 1 {
            reinstallSampler(node: nodes[0])
            return
        }
        bulkRunning = true
        testResult = TestResult(ok: true, message: "Reinstalling sampler on \(nodes.count) hosts…")
        Task {
            var okCount = 0
            var failures: [(String, String)] = []
            for node in nodes {
                do {
                    _ = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    okCount += 1
                } catch {
                    let msg = (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
                    failures.append((node.displayName, msg))
                }
            }
            await MainActor.run {
                bulkRunning = false
                if failures.isEmpty {
                    testResult = TestResult(ok: true, message: "Reinstalled sampler on \(okCount)/\(nodes.count) hosts")
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

/// The Servers table is extracted into its own view so its observation
/// subgraph only depends on NodeStore + ServerStore. Keeping the Toggle
/// that reads ServerSettings (`AutoUpdateToggleRow`) in the same parent
/// body as the Table used to trigger `AGGraphGetAttributeSubgraph`
/// precondition crashes on tab switch (macOS 15 / SwiftUI 6).
private struct ServersTable: View {
    @Environment(NodeStore.self) private var nodeStore
    @Environment(ServerStore.self) private var serverStore
    @Environment(SamplerUpdateCoordinator.self) private var samplerUpdater
    @Environment(\.backend) private var backend
    @Binding var selection: Set<Node.ID>

    var body: some View {
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
            TableColumn("Version") { n in
                versionCell(for: n)
            }
            TableColumn("Enabled") { n in
                Toggle("", isOn: Binding(
                    get: { n.enabled },
                    set: { newValue in
                        let b = backend
                        Task { try? await b?.setNodeEnabled(id: n.id, enabled: newValue) }
                    }
                ))
                .labelsHidden()
            }
        }
    }

    @ViewBuilder
    private func versionCell(for n: Node) -> some View {
        if samplerUpdater.isUpdating(id: n.id) {
            HStack(spacing: 4) {
                ProgressView().controlSize(.mini)
                Text("updating…")
                    .foregroundStyle(.secondary)
            }
        } else if let vm = serverStore.serverVMs.first(where: { $0.id == n.id }),
                  !vm.samplerVersion.isEmpty {
            let err = samplerUpdater.lastUpdateError[n.id]
            HStack(spacing: 4) {
                Text(vm.samplerVersion)
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(.secondary)
                if let err {
                    // Auto-update failed on this host — surface via a
                    // warning glyph with the exact error as a tooltip so
                    // the user doesn't have to open Console.app.
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(Color.orange)
                        .help("Auto-update failed: \(err)")
                }
            }
        } else {
            Text("—")
                .foregroundStyle(.secondary)
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
        case .suspended(let reason): return "paused: \(reason)"
        }
    }
}

/// Separate view on purpose — see the call site in ServersPane.
private struct AutoUpdateToggleRow: View {
    @Environment(ServerSettings.self) private var serverSettings

    var body: some View {
        Toggle("Auto-update remote samplers", isOn: Binding(
            get: { serverSettings.autoUpdateSamplersEnabled },
            set: { serverSettings.autoUpdateSamplersEnabled = $0; serverSettings.persist() }
        ))
        .help("When enabled, Towertail silently pushes the bundled sampler binary to any SSH host running an older build. Off by default — turn on only after verifying Test works for your hosts.")
    }
}
