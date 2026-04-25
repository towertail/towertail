import Foundation
import SwiftUI

@Observable
@MainActor
final class ServerStore {
    private(set) var serverVMs: [ServerViewModel] = []
    private let history: HistoryStore?
    /// Looks up the node config for a given VM id. Needed so aggregateState
    /// can respect per-node `contributesToMenuBarIcon`. Optional because
    /// tests and previews construct stores without a NodeStore.
    private let nodeLookup: (@MainActor (UUID) -> Node?)?
    var notifier: ThresholdNotifier?
    /// Last-logged aggregate state. Used to emit a single log entry when
    /// the menu-bar icon crosses warn/critical/nominal boundaries, rather
    /// than spamming every render. Not @Observable-tracked.
    @ObservationIgnored
    private var lastLoggedAggregate: AggregateState?

    init(
        history: HistoryStore? = nil,
        nodeLookup: (@MainActor (UUID) -> Node?)? = nil
    ) {
        self.history = history
        self.nodeLookup = nodeLookup
    }

    func register(_ vm: ServerViewModel) {
        if let history {
            // Scalar history is small (a few KB) and cheap to decode —
            // keep it synchronous so the very first render already has
            // CPU/MEM/DISK/NET lines drawn.
            let recent = history.loadRecent(nodeID: vm.id)
            vm.hydrate(from: recent)
            // Per-mount capacity and per-device I/O fit the same budget
            // (a few hundred KB at 2h × 10s poll) so we can hydrate them
            // synchronously too — the DISK tab is otherwise empty on
            // first open after a restart.
            let capRows = history.loadRecentDiskCapacity(nodeID: vm.id)
            vm.hydrateDiskCapacity(from: capRows)
            let ioRows = history.loadRecentDiskIO(nodeID: vm.id)
            vm.hydrateDiskIO(from: ioRows)
        }
        serverVMs.append(vm)
        // Proc history is deliberately NOT hydrated here. The popover and
        // cards never read it — only the full-view process table does —
        // and decoding ~5 MB of JSON per host at launch was pinning the
        // main thread for seconds while users were just trying to open
        // the menu bar. We hydrate lazily in `ensureProcsHydrated(for:)`
        // when the full view actually opens.
    }

    /// One-shot, on-demand hydration of a single host's proc history
    /// from SQLite. Safe to call multiple times: the first call kicks
    /// off a background decode, subsequent calls are no-ops.
    ///
    /// Called from `FullViewWindow.onAppear`. The small latency between
    /// window-open and proc-table-populated is acceptable — the table
    /// already shows a "Waiting for first process sample…" placeholder.
    func ensureProcsHydrated(for id: UUID) {
        guard let history, let vm = serverVMs.first(where: { $0.id == id }) else { return }
        if vm.procsHydrationStarted { return }
        vm.procsHydrationStarted = true
        Task.detached(priority: .utility) { [weak vm, history] in
            let rows = history.loadRecentProcs(nodeID: id)
            guard let vm else { return }
            await MainActor.run {
                vm.hydrateProcs(from: rows)
            }
        }
    }

    func ingest(_ sample: Sample, for id: UUID) {
        guard let vm = serverVMs.first(where: { $0.id == id }) else { return }
        let point = vm.ingest(sample)
        history?.append(nodeID: id, point: point)
        if let history, let ps = sample.procs {
            history.appendProcs(
                nodeID: id, t: sample.ts, root: ps.root, items: ps.items
            )
        }
        if let history, let disks = sample.disks, !disks.isEmpty {
            history.appendDiskCapacity(nodeID: id, t: sample.ts, mounts: disks)
        }
        if let history, let io = sample.diskIO {
            history.appendDiskIO(
                nodeID: id, t: sample.ts,
                totalReadBps: io.readBps, totalWriteBps: io.writeBps,
                devices: io.devices
            )
        }
        if let notifier, let node = nodeLookup?(id) {
            notifier.evaluate(vm: vm, node: node)
        }
        logAggregateIfChanged()
    }

    /// Bulk-ingest pre-generated samples. Used by MockCollector to
    /// backfill 2h of demo data in one main-actor hop instead of ~2,000
    /// individual hops (which pegged the main thread for ~10s at launch).
    /// Notifier evaluation and aggregate logging happen only once at the
    /// end, not per sample. When `skipPersistence` is true we bypass
    /// SQLite entirely — backfilled demo data is regenerated on every
    /// launch so writing it out just to read it back would be wasted work.
    func ingestBatch(_ items: [(UUID, Sample)], skipPersistence: Bool = false) {
        var lastVMByID: [UUID: ServerViewModel] = [:]
        for (id, sample) in items {
            guard let vm = serverVMs.first(where: { $0.id == id }) else { continue }
            let point = vm.ingest(sample)
            lastVMByID[id] = vm
            if !skipPersistence {
                history?.append(nodeID: id, point: point)
                if let history, let ps = sample.procs {
                    history.appendProcs(
                        nodeID: id, t: sample.ts, root: ps.root, items: ps.items
                    )
                }
                if let history, let disks = sample.disks, !disks.isEmpty {
                    history.appendDiskCapacity(nodeID: id, t: sample.ts, mounts: disks)
                }
                if let history, let io = sample.diskIO {
                    history.appendDiskIO(
                        nodeID: id, t: sample.ts,
                        totalReadBps: io.readBps, totalWriteBps: io.writeBps,
                        devices: io.devices
                    )
                }
            }
        }
        if let notifier {
            for (id, vm) in lastVMByID {
                if let node = nodeLookup?(id) {
                    notifier.evaluate(vm: vm, node: node)
                }
            }
        }
        logAggregateIfChanged()
    }

    func markOffline(id: UUID, reason: String, at t: Date) {
        guard let vm = serverVMs.first(where: { $0.id == id }) else { return }
        vm.markOffline(reason: reason, at: t)
        logAggregateIfChanged()
    }

    private func logAggregateIfChanged() {
        let now = aggregateState
        if lastLoggedAggregate == now { return }
        let prev = lastLoggedAggregate
        lastLoggedAggregate = now
        // Skip the very first emit (transition from nil → anything) so
        // log lines describe *transitions* the user sees, not startup
        // noise.
        guard let prev else { return }
        let s = summary
        Logger.shared.info(
            "icon: \(prev) → \(now)",
            category: "icon",
            kv: [
                "online": String(s.online),
                "warn": String(s.warn),
                "critical": String(s.critical),
                "down": String(s.down),
            ]
        )
    }

    /// Aggregate menu-bar state: honors per-node `iconOnWarn` /
    /// `iconOnCritical`. A VM in warn only escalates the icon if its node
    /// has iconOnWarn=true; likewise for critical. Nodes opted out still
    /// show in the popover and still notify — they just don't drag the
    /// top-level status.
    var aggregateState: AggregateState {
        var hasWarn = false
        var hasOfflineContributing = false
        var anyContributes = false
        for vm in serverVMs {
            let node = nodeLookup?(vm.id)
            let iconWarn = node?.iconOnWarn ?? true
            let iconCritical = node?.iconOnCritical ?? true
            switch vm.state {
            case .critical:
                if iconCritical {
                    return .critical
                }
            case .warn:
                if iconWarn { hasWarn = true }
            case .offline:
                // A host that has previously connected but is now offline
                // is treated as critical immediately — losing contact
                // with a known-good box is a real incident, not an
                // ambiguous "we never reached it" state. Hosts that have
                // never connected since being added stay just plain
                // offline (they're more likely a misconfiguration the
                // user is still working through).
                if iconCritical && vm.everConnected {
                    return .critical
                }
                if iconCritical {
                    hasOfflineContributing = true
                    anyContributes = true
                }
            default:
                break
            }
            if iconWarn || iconCritical { anyContributes = true }
        }
        if hasWarn { return .warn }
        if anyContributes, hasOfflineContributing {
            let allOffline = serverVMs
                .filter { (nodeLookup?($0.id)?.iconOnCritical ?? true) }
                .allSatisfy { $0.state.isOffline }
            if allOffline { return .critical }
        }
        return .nominal
    }

    var summary: (online: Int, warn: Int, critical: Int, down: Int) {
        var online = 0, warn = 0, critical = 0, down = 0
        for vm in serverVMs {
            switch vm.state {
            case .online: online += 1
            case .warn: warn += 1; online += 1
            case .critical: critical += 1; online += 1
            case .offline: down += 1
            // Suspended hosts are deliberately excluded from all four
            // buckets: we don't know their state, and showing them as
            // "down" (when really our Mac paused polling) is misleading.
            case .suspended: break
            case .unknown: break
            }
        }
        return (online, warn, critical, down)
    }
}
