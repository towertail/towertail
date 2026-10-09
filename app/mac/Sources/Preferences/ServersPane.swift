import SwiftUI

/// Server list on the left, settings for the selected server on the right.
struct ServersPane: View {
    @Binding var selection: Set<Node.ID>
    /// Opens the Alerts tab.
    let onEditDefaults: () -> Void

    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend

    @State private var tester = ServerTester()
    @State private var filter = ""
    @State private var showAdd = false
    @State private var showBulkImport = false
    @State private var confirmRemove = false

    private var filteredNodes: [Node] {
        let q = filter.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return nodeStore.nodes }
        return nodeStore.nodes.filter {
            $0.displayName.localizedCaseInsensitiveContains(q)
                || $0.userAtHost.localizedCaseInsensitiveContains(q)
                || $0.tags.contains { $0.localizedCaseInsensitiveContains(q) }
        }
    }

    private var selectedNodes: [Node] {
        nodeStore.nodes.filter { selection.contains($0.id) }
    }

    var body: some View {
        HStack(spacing: 0) {
            sidebar
                .frame(width: 240)
            Divider()
            VStack(spacing: 0) {
                detail
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                if let o = tester.outcome {
                    statusBar(o)
                }
            }
        }
        .onAppear {
            if selection.isEmpty, let first = nodeStore.nodes.first {
                selection = [first.id]
            }
        }
        .sheet(isPresented: $showAdd) {
            AddServerSheet { node in
                let b = backend
                Task { try? await b?.addNode(node) }
                selection = [node.id]
                showAdd = false
            } onCancel: {
                showAdd = false
            }
        }
        .sheet(isPresented: $showBulkImport) {
            BulkImportWizard()
        }
        .confirmationDialog(removeTitle, isPresented: $confirmRemove) {
            Button("Remove", role: .destructive, action: removeSelected)
        } message: {
            Text("You cannot undo this.")
        }
    }

    // MARK: sidebar

    private var sidebar: some View {
        VStack(spacing: 0) {
            TextField("Filter", text: $filter, prompt: Text("Filter"))
                .textFieldStyle(.roundedBorder)
                .padding(8)
            List(filteredNodes, selection: $selection) { node in
                ServerRow(node: node)
                    .tag(node.id)
            }
            .listStyle(.sidebar)
            Divider()
            HStack(spacing: 2) {
                Button { showAdd = true } label: {
                    Image(systemName: "plus").frame(width: 22, height: 18)
                }
                .help("Add a server")
                Button { confirmRemove = true } label: {
                    Image(systemName: "minus").frame(width: 22, height: 18)
                }
                .disabled(selection.isEmpty)
                .help("Remove the selected servers")
                Divider().frame(height: 14).padding(.horizontal, 4)
                Button("Import…") { showBulkImport = true }
                    .help("Import servers from Tailscale, a CSV file, or a pasted host list.")
                Spacer()
                Menu {
                    Button(selection.count > 1 ? "Test \(selection.count) Servers" : "Test") {
                        tester.test(selectedNodes, backend: backend, nodeStore: nodeStore)
                    }
                    .disabled(selection.isEmpty || tester.running)
                    Button(selection.count > 1 ? "Reinstall Sampler on \(selection.count) Servers" : "Reinstall Sampler") {
                        tester.reinstall(selectedNodes)
                    }
                    .disabled(!selectedNodes.contains { $0.kind == .ssh } || tester.running)
                    Divider()
                    AutoUpdateToggle()
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuIndicator(.hidden)
                .fixedSize()
                .help("More actions")
            }
            .buttonStyle(.borderless)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
        }
    }

    // MARK: detail

    @ViewBuilder
    private var detail: some View {
        if selectedNodes.count == 1, let node = selectedNodes.first {
            ServerDetailView(node: node, tester: tester, onEditDefaults: onEditDefaults)
                .id(node.id)
        } else if selectedNodes.count > 1 {
            VStack(spacing: 12) {
                Text("\(selectedNodes.count) servers selected")
                    .font(.title3)
                HStack {
                    Button("Test") { tester.test(selectedNodes, backend: backend, nodeStore: nodeStore) }
                    Button("Reinstall Sampler") { tester.reinstall(selectedNodes) }
                        .disabled(!selectedNodes.contains { $0.kind == .ssh })
                }
                .disabled(tester.running)
            }
        } else {
            ContentUnavailableView {
                Label("No Server Selected", systemImage: "server.rack")
            } actions: {
                Button("Add Server") { showAdd = true }
            }
        }
    }

    private func statusBar(_ o: ServerTester.Outcome) -> some View {
        HStack(spacing: 6) {
            if tester.running {
                ProgressView().controlSize(.small)
            } else {
                Image(systemName: o.ok ? "checkmark.circle.fill" : "xmark.octagon.fill")
                    .foregroundStyle(o.ok ? ThresholdTint.nominal.color : ThresholdTint.critical.color)
            }
            Text(o.message)
                .font(.callout)
                .lineLimit(2)
                .textSelection(.enabled)
            Spacer()
            Button { tester.clear() } label: { Image(systemName: "xmark") }
                .buttonStyle(.borderless)
                .disabled(tester.running)
                .help("Dismiss")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(.bar)
    }

    private var removeTitle: String {
        selectedNodes.count == 1 ? "Remove \(selectedNodes[0].displayName)?" : "Remove \(selectedNodes.count) servers?"
    }

    private func removeSelected() {
        let ids = selection
        let b = backend
        Task {
            for id in ids { try? await b?.removeNode(id: id) }
        }
        selection = []
    }
}

/// One server in the sidebar: status dot, name, address and badges.
private struct ServerRow: View {
    let node: Node

    @Environment(ServerStore.self) private var serverStore
    @Environment(SamplerUpdateCoordinator.self) private var samplerUpdater

    var body: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(dotColor)
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 1) {
                Text(node.displayName)
                    .lineLimit(1)
                Text(node.kind == .local ? "This Mac" : node.userAtHost)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: 4)
            if samplerUpdater.isUpdating(id: node.id) {
                ProgressView().controlSize(.mini)
            } else if samplerUpdater.lastUpdateError[node.id] != nil {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(ThresholdTint.warn.color)
                    .help("Sampler auto-update failed")
            }
            if node.thresholdOverrides != nil || node.customAlerts != nil {
                Text("custom")
                    .font(.caption2)
                    .padding(.horizontal, 4)
                    .background(RoundedRectangle(cornerRadius: 3).fill(Color.accentColor.opacity(0.15)))
                    .help("Has custom alert rules")
            }
        }
        .opacity(node.enabled ? 1 : 0.5)
        .padding(.vertical, 2)
    }

    private var dotColor: Color {
        guard node.enabled, let vm = serverStore.serverVMs.first(where: { $0.id == node.id }) else {
            return .secondary.opacity(0.5)
        }
        return vm.statusDotColor
    }
}

/// Kept in its own view so the ServerSettings read stays out of the list's
/// observation graph (see PreferencesWindow).
private struct AutoUpdateToggle: View {
    @Environment(ServerSettings.self) private var serverSettings

    var body: some View {
        Toggle("Auto-update Remote Samplers", isOn: Binding(
            get: { serverSettings.autoUpdateSamplersEnabled },
            set: { serverSettings.autoUpdateSamplersEnabled = $0; serverSettings.persist() }
        ))
    }
}
