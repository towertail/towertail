import Foundation

final class RealCollector: Collector {
    let nodeStore: NodeStore
    let settings: AppSettings
    let history: HistoryStore?
    let invokerFactory: @Sendable (Node) -> AgentInvoker

    init(
        nodeStore: NodeStore,
        settings: AppSettings,
        history: HistoryStore? = nil,
        invokerFactory: @escaping @Sendable (Node) -> AgentInvoker = makeInvoker(for:)
    ) {
        self.nodeStore = nodeStore
        self.settings = settings
        self.history = history
        self.invokerFactory = invokerFactory
    }

    func run(sink: ServerStore) async {
        // Supervisor loop: reconciles the set of per-node polling tasks with
        // the current node list. Each node has its own pacer driven by
        // settings.pollingInterval(for:) so kinds can tick at different rates.
        var tasks: [UUID: Task<Void, Never>] = [:]
        defer { tasks.values.forEach { $0.cancel() } }

        while !Task.isCancelled {
            let snapshot = await MainActor.run { nodeStore.nodes }
            await MainActor.run {
                Self.syncViewModels(for: snapshot, store: sink, settings: settings)
            }
            let enabledIDs = Set(snapshot.filter(\.enabled).map(\.id))

            // Cancel tasks for removed/disabled nodes.
            for (id, task) in tasks where !enabledIDs.contains(id) {
                task.cancel()
                tasks[id] = nil
            }
            // Spawn per-node pacers for newly enabled nodes.
            for node in snapshot where node.enabled && tasks[node.id] == nil {
                let factory = self.invokerFactory
                let settings = self.settings
                let history = self.history
                tasks[node.id] = Task.detached(priority: .utility) {
                    await Self.pacer(node: node, factory: factory, sink: sink, settings: settings, history: history)
                }
            }

            // Re-check node list periodically (pick up adds/removes/kind changes).
            try? await Task.sleep(nanoseconds: 2_000_000_000)
        }
    }

    /// Per-node polling loop. Respects the node's kind-specific interval on
    /// every tick so slider changes apply immediately.
    private static func pacer(
        node: Node,
        factory: @Sendable (Node) -> AgentInvoker,
        sink: ServerStore,
        settings: AppSettings,
        history: HistoryStore?
    ) async {
        let kind = node.kind
        let invoker = factory(node)
        while !Task.isCancelled {
            do {
                let sample = try await invoker.invokeOnce(node: node)
                await MainActor.run {
                    sink.ingest(sample, for: node.id)
                }
            } catch {
                let reason = shortReason(for: error)
                await MainActor.run {
                    sink.markOffline(id: node.id, reason: reason, at: Date())
                }
            }
            let intervalSec = await MainActor.run { settings.pollingInterval(for: kind) }
            let nanos = UInt64(max(1, intervalSec)) * 1_000_000_000
            try? await Task.sleep(nanoseconds: nanos)
        }
        _ = history // retained; trimming hook belongs here if we add one later
    }

    @MainActor
    private static func syncViewModels(for nodes: [Node], store: ServerStore, settings: AppSettings) {
        let existing = Set(store.serverVMs.map(\.id))
        let byID: [UUID: Node] = Dictionary(uniqueKeysWithValues: nodes.map { ($0.id, $0) })
        for node in nodes where !existing.contains(node.id) {
            let vm = ServerViewModel(
                id: node.id,
                hostname: node.displayName,
                dnsName: node.kind == .ssh ? node.userAtHost : "local",
                osArch: node.kind == .local ? "macOS" : "—",
                kind: node.kind,
                thresholds: node.customThresholds ?? settings.thresholds
            )
            store.register(vm)
        }
        // On every sync, push the currently-effective thresholds per VM —
        // node override takes precedence, otherwise the global. This also
        // picks up edits to either the global sliders or a node's custom
        // set without waiting for the next collector tick.
        for vm in store.serverVMs {
            if let node = byID[vm.id], let custom = node.customThresholds {
                vm.thresholds = custom
            } else {
                vm.thresholds = settings.thresholds
            }
        }
    }
}

private func shortReason(for error: Error) -> String {
    if let agentErr = error as? AgentInvokeError {
        switch agentErr {
        case .binaryMissing: return "agent binary missing"
        case .timeout: return "timeout"
        case .emptyOutput: return "no output"
        case .sshFailed(let stderr, let code):
            let firstLine = stderr
                .split(whereSeparator: { $0.isNewline })
                .first
                .map(String.init)?
                .trimmingCharacters(in: .whitespaces)
            if let firstLine, !firstLine.isEmpty { return firstLine }
            return "exit \(code)"
        case .decodeFailed: return "decode failed"
        case .misconfigured(let reason): return reason
        }
    }
    return error.localizedDescription
}
