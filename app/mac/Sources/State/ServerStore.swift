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
            let recent = history.loadRecent(nodeID: vm.id)
            vm.hydrate(from: recent)
            let procs = history.loadRecentProcs(nodeID: vm.id)
            vm.hydrateProcs(from: procs)
        }
        serverVMs.append(vm)
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
        if let notifier, let node = nodeLookup?(id) {
            notifier.evaluate(vm: vm, node: node)
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
                // Offline counts for the "all offline → critical" escalation
                // only when that node would be allowed to show critical.
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
            case .unknown: break
            }
        }
        return (online, warn, critical, down)
    }
}
