import Foundation

final class RealCollector: Collector {
    let nodeStore: NodeStore
    let settings: ServerSettings
    let history: HistoryStore?
    let invokerFactory: @Sendable (Node) -> SamplerInvoker
    let samplerUpdater: SamplerUpdateCoordinator?
    /// Gates polling on Mac sleep / local network state. Optional so tests
    /// and the Mock collector path don't need to construct one — when nil,
    /// the pacer polls unconditionally (the old behavior).
    let reachability: SystemReachabilityMonitor?

    init(
        nodeStore: NodeStore,
        settings: ServerSettings,
        history: HistoryStore? = nil,
        samplerUpdater: SamplerUpdateCoordinator? = nil,
        reachability: SystemReachabilityMonitor? = nil,
        invokerFactory: @escaping @Sendable (Node) -> SamplerInvoker = makeInvoker(for:)
    ) {
        self.nodeStore = nodeStore
        self.settings = settings
        self.history = history
        self.samplerUpdater = samplerUpdater
        self.reachability = reachability
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
                let updater = self.samplerUpdater
                let reachability = self.reachability
                tasks[node.id] = Task.detached(priority: .utility) {
                    await Self.pacer(
                        node: node,
                        factory: factory,
                        sink: sink,
                        settings: settings,
                        history: history,
                        samplerUpdater: updater,
                        reachability: reachability
                    )
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
        factory: @Sendable (Node) -> SamplerInvoker,
        sink: ServerStore,
        settings: ServerSettings,
        history: HistoryStore?,
        samplerUpdater: SamplerUpdateCoordinator?,
        reachability: SystemReachabilityMonitor?
    ) async {
        let kind = node.kind
        let invoker = factory(node)
        // Only log online↔offline transitions, not every sample — otherwise
        // the log grows by a line every 2-10 seconds per host.
        var lastWasSuccess: Bool? = nil
        // Tracks whether this pacer has marked the VM suspended, so the
        // next resumed tick can clear the badge on a single main-hop.
        var wasSuspended = false
        while !Task.isCancelled {
            let gate = await Self.gate(reachability: reachability)
            if let reason = gate {
                // Reachability says don't even try — park the VM and sleep
                // a short slice before re-checking. Don't spawn ssh; don't
                // burn battery. Sleep is short so we're responsive when
                // availability returns.
                if !wasSuspended {
                    await MainActor.run {
                        if let vm = sink.serverVMs.first(where: { $0.id == node.id }) {
                            vm.markSuspended(reason: reason)
                        }
                    }
                    wasSuspended = true
                    lastWasSuccess = nil
                }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                continue
            }
            if wasSuspended {
                await MainActor.run {
                    if let vm = sink.serverVMs.first(where: { $0.id == node.id }) {
                        vm.clearSuspended()
                    }
                }
                wasSuspended = false
            }
            do {
                let sample = try await invoker.invokeOnce(node: node)
                await MainActor.run {
                    sink.ingest(sample, for: node.id)
                    let enabled = settings.autoUpdateSamplersEnabled
                    samplerUpdater?.maybeUpdate(
                        node: node,
                        reportedSampler: sample.host.sampler,
                        enabled: enabled
                    )
                    if lastWasSuccess == false {
                        Logger.shared.info(
                            "sample: recovered",
                            category: "sample",
                            hostID: node.id, host: node.displayName,
                            kv: ["kind": kind.rawValue]
                        )
                    }
                }
                lastWasSuccess = true
            } catch {
                let reason = shortReason(for: error)
                await MainActor.run {
                    sink.markOffline(id: node.id, reason: reason, at: Date())
                    if lastWasSuccess != false {
                        Logger.shared.warn(
                            "sample: failed",
                            category: "sample",
                            hostID: node.id, host: node.displayName,
                            kv: ["kind": kind.rawValue, "reason": reason]
                        )
                    }
                }
                lastWasSuccess = false
            }
            let intervalSec = await MainActor.run { settings.pollingInterval(for: kind) }
            let nanos = UInt64(max(1, intervalSec)) * 1_000_000_000
            try? await Task.sleep(nanoseconds: nanos)
        }
        _ = history // retained; trimming hook belongs here if we add one later
    }

    /// Asks the reachability monitor (on the main actor, where it lives)
    /// whether polling is allowed right now, and if not, what reason to
    /// show on the card. Returning nil means "go ahead and poll".
    private static func gate(reachability: SystemReachabilityMonitor?) async -> String? {
        guard let reachability else { return nil }
        return await MainActor.run {
            let a = reachability.availability
            if a.shouldPoll { return nil as String? }
            return a.suspendedReason
        }
    }

    @MainActor
    private static func syncViewModels(for nodes: [Node], store: ServerStore, settings: ServerSettings) {
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
    if let samplerErr = error as? SamplerInvokeError {
        switch samplerErr {
        case .binaryMissing: return "sampler binary missing"
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
