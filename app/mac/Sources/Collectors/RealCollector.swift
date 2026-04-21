import Foundation

final class RealCollector: Collector {
    let nodeStore: NodeStore
    let settings: AppSettings
    let invokerFactory: @Sendable (Node) -> AgentInvoker

    init(
        nodeStore: NodeStore,
        settings: AppSettings,
        invokerFactory: @escaping @Sendable (Node) -> AgentInvoker = makeInvoker(for:)
    ) {
        self.nodeStore = nodeStore
        self.settings = settings
        self.invokerFactory = invokerFactory
    }

    func run(sink: ServerStore) async {
        while !Task.isCancelled {
            let snapshot = await MainActor.run { nodeStore.nodes }
            let intervalSec = await MainActor.run { settings.pollingIntervalSeconds }

            await MainActor.run {
                Self.syncViewModels(for: snapshot, store: sink, settings: settings)
            }

            await withTaskGroup(of: Void.self) { group in
                for node in snapshot where node.enabled {
                    let factory = self.invokerFactory
                    let sinkRef = sink
                    group.addTask {
                        await Self.pollOne(node: node, factory: factory, sink: sinkRef)
                    }
                }
            }

            let nanos = UInt64(max(1, intervalSec)) * 1_000_000_000
            try? await Task.sleep(nanoseconds: nanos)
        }
    }

    private static func pollOne(
        node: Node,
        factory: @Sendable (Node) -> AgentInvoker,
        sink: ServerStore
    ) async {
        let invoker = factory(node)
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
    }

    @MainActor
    private static func syncViewModels(for nodes: [Node], store: ServerStore, settings: AppSettings) {
        let existing = Set(store.serverVMs.map(\.id))
        for node in nodes where !existing.contains(node.id) {
            let vm = ServerViewModel(
                id: node.id,
                hostname: node.displayName,
                dnsName: node.kind == .ssh ? node.userAtHost : "local",
                osArch: node.kind == .local ? "macOS" : "—",
                thresholds: settings.thresholds
            )
            store.register(vm)
        }
        for vm in store.serverVMs {
            vm.thresholds = settings.thresholds
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
