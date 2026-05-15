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
    /// Node ids the supervisor should drop and re-spawn on its next tick.
    /// Written from the main actor (Preferences UI after a successful
    /// Test) and drained inside the supervisor loop. Wrapped in a lock
    /// because the supervisor runs on a detached utility task — without
    /// this, Swift concurrency rejects the cross-isolation read.
    private let respawnLock = NSLock()
    private var pendingRespawns: Set<UUID> = []

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

    func respawnPacer(id: UUID) {
        respawnLock.lock()
        pendingRespawns.insert(id)
        respawnLock.unlock()
    }

    /// Drains and clears the queue. Returns the ids that were pending
    /// when the call ran. Called only from the supervisor loop.
    private func takePendingRespawns() -> Set<UUID> {
        respawnLock.lock()
        let ids = pendingRespawns
        pendingRespawns.removeAll(keepingCapacity: true)
        respawnLock.unlock()
        return ids
    }

    func run(sink: ServerStore) async {
        // Supervisor loop: reconciles the set of per-node polling tasks with
        // the current node list. Each node has its own pacer driven by
        // settings.pollingInterval(for:) so kinds can tick at different rates.
        //
        // We keep the captured Node alongside the task so we can detect
        // config edits and respawn. We also keep entries for pacers that
        // exited (after a fatal error); they sit parked until the user
        // edits the node, at which point the diff trips a respawn.
        struct Entry {
            var task: Task<Void, Never>
            var node: Node
        }
        var entries: [UUID: Entry] = [:]
        defer { entries.values.forEach { $0.task.cancel() } }

        while !Task.isCancelled {
            let snapshot = await MainActor.run { nodeStore.nodes }
            await MainActor.run {
                Self.syncViewModels(for: snapshot, store: sink, settings: settings)
            }
            let enabledIDs = Set(snapshot.filter(\.enabled).map(\.id))
            let byID: [UUID: Node] = Dictionary(uniqueKeysWithValues: snapshot.map { ($0.id, $0) })

            // Drop tasks for removed/disabled nodes.
            for (id, entry) in entries where !enabledIDs.contains(id) {
                entry.task.cancel()
                entries[id] = nil
            }
            // Drop tasks whose node config changed; the spawn loop below
            // will start a fresh pacer with the new value. This is the
            // retry trigger after a permanent error: editing the node
            // (e.g. switching auth method) clears the halted entry.
            for (id, entry) in entries {
                if let current = byID[id], current != entry.node {
                    entry.task.cancel()
                    entries[id] = nil
                }
            }
            // Drain any explicit respawn requests (e.g. Preferences →
            // Test succeeded on a host whose pacer halted on a permanent
            // error). Cancel the parked task so the spawn loop below
            // starts a fresh pacer with the current node config.
            let respawns = takePendingRespawns()
            for id in respawns {
                if let entry = entries[id] {
                    entry.task.cancel()
                    entries[id] = nil
                }
            }
            // Spawn per-node pacers for nodes without a live entry. An
            // exited (halted-on-error) entry blocks respawn until its node
            // config changes — otherwise we'd hammer a host that just
            // refused our credentials every 2 seconds.
            for node in snapshot where node.enabled && entries[node.id] == nil {
                let factory = self.invokerFactory
                let settings = self.settings
                let history = self.history
                let updater = self.samplerUpdater
                let reachability = self.reachability
                let nodeStore = self.nodeStore
                let task = Task.detached(priority: .utility) {
                    await Self.pacer(
                        node: node,
                        factory: factory,
                        sink: sink,
                        settings: settings,
                        history: history,
                        samplerUpdater: updater,
                        reachability: reachability,
                        nodeStore: nodeStore
                    )
                }
                entries[node.id] = Entry(task: task, node: node)
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
        reachability: SystemReachabilityMonitor?,
        nodeStore: NodeStore
    ) async {
        let kind = node.kind
        let invoker = factory(node)
        // Tracks whether this pacer has marked the VM suspended, so the
        // next resumed tick can clear the badge on a single main-hop.
        var wasSuspended = false
        // Retry policy depends on whether we've ever talked to this host.
        //
        //   Never connected → likely a real misconfig the user needs to
        //     fix (wrong port, firewall, sampler not installed). Try
        //     `maxColdAttempts` times with short exponential backoff,
        //     then halt — the supervisor respawns when the node is
        //     edited (NodeStore.update → snapshot diff).
        //
        //   Connected at least once → host is real and was reachable.
        //     Treat outages as transient (laptop sleep, Tailscale flap,
        //     server reboot, NAT eviction) and retry indefinitely with
        //     exponential backoff capped at `maxBackoffSec`. So a host
        //     that comes back hours later still recovers automatically
        //     without user intervention.
        //
        // Counter resets to 0 on any successful sample.
        var transientFailures = 0
        // Seed from disk so a host that connected on a previous launch
        // gets the indefinite-retry treatment immediately at startup,
        // even if it never recovers in this session.
        var everConnected = node.lastSuccessfulConnect != nil
        let maxColdAttempts = 3
        let coldBaseSec: Double = 2          // 2, 4, 8 → halts after ~14s
        let warmBaseSec: Double = 10         // 10, 20, 40, 80, … capped
        let maxBackoffSec: Double = 30 * 60  // 30 min ceiling once warm
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
                transientFailures = 0
                let firstSuccess = !everConnected
                everConnected = true
                await MainActor.run {
                    sink.ingest(sample, for: node.id)
                    if let vm = sink.serverVMs.first(where: { $0.id == node.id }) {
                        vm.everConnected = true
                    }
                    if firstSuccess {
                        // Persist the warm flag exactly once — subsequent
                        // successes keep `everConnected` true in memory
                        // without churning settings.json.
                        nodeStore.markConnected(id: node.id, at: sample.ts)
                    }
                    let enabled = settings.autoUpdateSamplersEnabled
                    samplerUpdater?.maybeUpdate(
                        node: node,
                        reportedSampler: sample.host.sampler,
                        enabled: enabled
                    )
                }
            } catch {
                let reason = shortReason(for: error)
                let permanent = isPermanentError(error)
                let attempt = permanent ? 0 : transientFailures + 1
                // Cold-start exhaustion only applies to hosts we've never
                // talked to. Once warm, we keep retrying with longer and
                // longer backoff until the user disables the node.
                let exhausted = !permanent && !everConnected && attempt >= maxColdAttempts

                let backoffSec: Double = {
                    if permanent || exhausted { return 0 }
                    if everConnected {
                        // Warm: 10s, 20s, 40s, 80s, … capped at 30 min.
                        let raw = warmBaseSec * pow(2.0, Double(attempt - 1))
                        return min(maxBackoffSec, raw)
                    } else {
                        // Cold: 2s, 4s, 8s — short retries before halting.
                        return coldBaseSec * pow(2.0, Double(attempt - 1))
                    }
                }()

                await MainActor.run {
                    sink.markOffline(id: node.id, reason: reason, at: Date())
                    let msg: String
                    if permanent {
                        msg = "sample: halting on permanent error"
                    } else if exhausted {
                        msg = "sample: halting after \(maxColdAttempts) cold-start retries"
                    } else if everConnected {
                        msg = "sample: transient error, retrying in \(Int(backoffSec))s (warm)"
                    } else {
                        msg = "sample: transient error, retrying in \(Int(backoffSec))s (\(attempt)/\(maxColdAttempts))"
                    }
                    Logger.shared.warn(
                        msg,
                        category: "sample",
                        hostID: node.id, host: node.displayName,
                        kv: [
                            "kind": kind.rawValue,
                            "reason": reason,
                            "transient_failures": String(attempt),
                            "ever_connected": String(everConnected),
                        ]
                    )
                }
                if permanent || exhausted {
                    // Permanent: auth / host-key / misconfig — retry
                    // doesn't help and Citadel leaks the underlying TCP
                    // socket when its connect chain fails before
                    // `.authenticated`.
                    // Exhausted: a never-reached host has missed
                    // `maxColdAttempts` connects in a row, almost certainly
                    // a config problem. Stop hammering; the supervisor
                    // respawns once the user edits the node.
                    return
                }
                transientFailures = attempt
                try? await Task.sleep(nanoseconds: UInt64(backoffSec * 1_000_000_000))
                continue
            }
            let intervalSec = await MainActor.run { settings.pollingInterval(for: kind) }
            let nanos = UInt64(max(1, intervalSec)) * 1_000_000_000
            try? await Task.sleep(nanoseconds: nanos)
        }
        _ = history // retained; trimming hook belongs here if we add one later
    }

    /// Errors that won't change on retry. Auth failures, host-key
    /// mismatches, and node-misconfiguration are user-fixes; everything
    /// else (network blips, channel resets, sampler crashes, decode
    /// errors from a partial transfer) is treated as transient and
    /// retried with backoff.
    private static func isPermanentError(_ error: Error) -> Bool {
        // Our own taxonomy: only `misconfigured` is permanent. The other
        // SamplerInvokeError cases (sshFailed, timeout, emptyOutput,
        // decodeFailed) all happen on transient remote/network conditions
        // — a server that briefly returns nothing or exits non-zero will
        // usually recover on its own.
        if let e = error as? SamplerInvokeError {
            if case .misconfigured = e { return true }
            return false
        }
        // SSHConnectionFactory's auth/host-key wrappers are surfaces for
        // user-correctable problems. Don't burn battery hammering a host
        // that just refused our credentials.
        if error is SSHConnectionFactory.HostKeyMismatch { return true }
        if error is SSHConnectionFactory.HostKeyRejected { return true }
        if error is SSHConnectionFactory.NoCredential { return true }
        // Anything else (NIOCore.ChannelError, Citadel.TTYSTDError,
        // POSIXError, etc.) is treated as transient.
        return false
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
                thresholds: MetricThresholds.effective(global: settings.thresholds, override: node.customThresholds)
            )
            // Local nodes are always considered "warm" — the sampler is
            // a bundled binary on the same machine, so an offline reading
            // is genuinely a problem worth escalating, not a fresh
            // never-tried install.
            vm.everConnected = node.lastSuccessfulConnect != nil || node.kind == .local
            store.register(vm)
        }
        // On every sync, push the currently-effective thresholds per VM —
        // node override takes precedence, otherwise the global. This also
        // picks up edits to either the global sliders or a node's custom
        // set without waiting for the next collector tick.
        for vm in store.serverVMs {
            let override = byID[vm.id]?.customThresholds
            vm.thresholds = MetricThresholds.effective(global: settings.thresholds, override: override)
            // Refresh the warm flag from the persisted node — handles the
            // case where the node was added in this session and just got
            // its first stamp (the pacer also flips the in-memory flag,
            // so this is mostly the relaunch path).
            if let node = byID[vm.id], node.lastSuccessfulConnect != nil {
                vm.everConnected = true
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
    return SSHErrorRenderer.describe(error)
}
