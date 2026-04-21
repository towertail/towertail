import SwiftUI

struct ServersPane: View {
    @Environment(NodeStore.self) private var nodeStore
    @Environment(ServerStore.self) private var serverStore

    @State private var selection: Node.ID?
    @State private var sheet: ServerEditSheetContext?
    @State private var testResult: TestResult?

    private struct TestResult: Identifiable {
        let id = UUID()
        let ok: Bool
        let message: String
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
                Button {
                    if let id = selection {
                        nodeStore.remove(id: id)
                        selection = nil
                    }
                } label: {
                    Image(systemName: "minus")
                }
                .disabled(selection == nil)

                Button {
                    if let id = selection, let node = nodeStore.node(withId: id) {
                        sheet = .edit(node)
                    }
                } label: {
                    Text("Edit")
                }
                .disabled(selection == nil)

                Button {
                    if let id = selection, let node = nodeStore.node(withId: id) {
                        runTest(node: node)
                    }
                } label: {
                    Text("Test")
                }
                .disabled(selection == nil)

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
            Text("SSH nodes require the agent at `~/.towertail/agent` on the remote host (v1). Auto-bootstrap is planned for a later release. Key-based auth only; add the relevant key to `~/.ssh/config` or an ssh-agent.")
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

    private func runTest(node: Node) {
        testResult = TestResult(ok: true, message: "Testing \(node.displayName)…")
        Task {
            let invoker = makeInvoker(for: node)
            do {
                let sample = try await invoker.invokeOnce(node: node)
                await MainActor.run {
                    testResult = TestResult(
                        ok: true,
                        message: "OK: \(sample.host.name) · cpu \(Int(round(sample.cpu.pct)))% · cores \(sample.cpu.cores)"
                    )
                }
            } catch {
                let msg = (error as? AgentInvokeError)?.errorDescription ?? error.localizedDescription
                await MainActor.run {
                    testResult = TestResult(ok: false, message: msg)
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
