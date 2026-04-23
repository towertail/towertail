import Foundation

/// Pure filtering / sorting logic that drives the popover server list.
/// Lives apart from `PopoverRoot` so it can be unit-tested and reused
/// (e.g. by a future dashboard widget) without a SwiftUI host.
///
/// `@MainActor` because `ServerViewModel` is — reading its `state`,
/// `hostname`, `dnsName` from a nonisolated context would otherwise need
/// a hop. The view body that calls `apply(...)` is already on the main
/// actor, so this is free in practice.
@MainActor
enum ServerFilter {
    /// Higher rank ⇒ surfaces nearer the top. Suspended/unknown rank
    /// negative because they aren't "something's wrong" states.
    static func severityRank(_ s: ServerConnState) -> Int {
        switch s {
        case .critical: return 3
        case .offline: return 2
        case .warn: return 1
        case .online: return 0
        case .suspended: return -1
        case .unknown: return -1
        }
    }

    /// Top-level entry point. Applies pill filter, search, then floats
    /// favorites. Order matters: search runs over the severity-ranked
    /// slice so favorites stay top-of-list within their tier.
    static func apply(
        vms: [ServerViewModel],
        filter: PopoverFilter,
        query: String,
        nodeLookup: (UUID) -> Node?
    ) -> [ServerViewModel] {
        let bySeverity = filterAndSort(vms, by: filter)
        let afterSearch = applySearch(bySeverity, query: query, nodeLookup: nodeLookup)
        return floatFavorites(afterSearch, nodeLookup: nodeLookup)
    }

    // MARK: - Pieces (exposed for testing)

    static func filterAndSort(_ vms: [ServerViewModel], by filter: PopoverFilter) -> [ServerViewModel] {
        switch filter {
        case .all:
            return sortBySeverityStable(vms)
        case .online:
            // Healthy only — warn/critical are excluded even though they're
            // technically reachable, because the Online pill means "show me
            // the ones that are actually fine."
            return vms.filter { if case .online = $0.state { return true } else { return false } }
        case .warn:
            let problems = vms.filter { vm in
                switch vm.state {
                case .warn, .critical: return true
                default: return false
                }
            }
            return sortBySeverityStable(problems)
        case .down:
            return vms.filter { if case .offline = $0.state { return true } else { return false } }
        }
    }

    /// Severity-descending with index tiebreak. Swift's sort isn't stable,
    /// so the explicit offset preserves registration order within a tier.
    private static func sortBySeverityStable(_ vms: [ServerViewModel]) -> [ServerViewModel] {
        vms.enumerated()
            .sorted { lhs, rhs in
                let ra = severityRank(lhs.element.state)
                let rb = severityRank(rhs.element.state)
                if ra != rb { return ra > rb }
                return lhs.offset < rhs.offset
            }
            .map(\.element)
    }

    static func applySearch(
        _ vms: [ServerViewModel],
        query: String,
        nodeLookup: (UUID) -> Node?
    ) -> [ServerViewModel] {
        let q = query.normalizedForSearch()
        guard !q.isEmpty else { return vms }
        return vms.filter { vm in
            if vm.hostname.lowercased().contains(q) { return true }
            if vm.dnsName.lowercased().contains(q) { return true }
            if let n = nodeLookup(vm.id) {
                if n.displayName.lowercased().contains(q) { return true }
                if n.tags.contains(where: { $0.lowercased().contains(q) }) { return true }
                if let h = n.sshHost, h.lowercased().contains(q) { return true }
            }
            return false
        }
    }

    /// Stable-partition favorites to the top while preserving the prior
    /// ordering within each group — keeps severity-sorted `warn` ordering
    /// intact, only pulls pinned hosts up.
    static func floatFavorites(
        _ vms: [ServerViewModel],
        nodeLookup: (UUID) -> Node?
    ) -> [ServerViewModel] {
        var favorites: [ServerViewModel] = []
        var rest: [ServerViewModel] = []
        for vm in vms {
            if nodeLookup(vm.id)?.favorite == true {
                favorites.append(vm)
            } else {
                rest.append(vm)
            }
        }
        return favorites + rest
    }
}
