import SwiftUI

enum PopoverFilter: Hashable {
    case all
    case online
    case warn
    case down
}

private func severityRank(_ s: ServerConnState) -> Int {
    switch s {
    case .critical: return 3
    case .offline: return 2
    case .warn: return 1
    case .online: return 0
    // Suspended ranks with unknown — we have no live data, but it's not
    // a "something's wrong" state like offline/warn/critical.
    case .suspended: return -1
    case .unknown: return -1
    }
}

struct PopoverRoot: View {
    @Environment(ServerStore.self) private var store
    @Environment(NodeStore.self) private var nodeStore
    @State private var filter: PopoverFilter = .all
    /// Live text from the search field. Changes drive a 400ms debounce
    /// task that copies into `debouncedQuery` — typing doesn't rebuild the
    /// card list on every keystroke.
    @State private var searchText: String = ""
    @State private var debouncedQuery: String = ""
    @State private var debounceTask: Task<Void, Never>?

    var body: some View {
        VStack(spacing: 0) {
            PopoverHeader(filter: $filter, searchText: $searchText)
            Divider().opacity(0.3)
            ScrollView {
                LazyVStack(spacing: 10) {
                    ForEach(filteredVMs) { vm in
                        ServerCardView(vm: vm)
                    }
                }
                .padding(10)
            }
        }
        .frame(width: 360, height: 620)
        .background(Color(nsColor: .windowBackgroundColor))
        .onChange(of: searchText) { _, newValue in
            debounceTask?.cancel()
            // Empty transitions should apply immediately — hitting the X
            // or Escape feels broken if it lags 400ms behind.
            if newValue.isEmpty {
                debouncedQuery = ""
                return
            }
            debounceTask = Task { @MainActor in
                try? await Task.sleep(nanoseconds: 400_000_000)
                if Task.isCancelled { return }
                debouncedQuery = newValue
            }
        }
    }

    private var filteredVMs: [ServerViewModel] {
        let bySeverity: [ServerViewModel]
        switch filter {
        case .all:
            // Escalate critical, then warn, to the top so problems are seen
            // first. Swift's sort isn't guaranteed stable, so tie-break on
            // the original index to preserve registration order within a
            // severity tier.
            bySeverity = store.serverVMs
                .enumerated()
                .sorted { lhs, rhs in
                    let ra = severityRank(lhs.element.state)
                    let rb = severityRank(rhs.element.state)
                    if ra != rb { return ra > rb }
                    return lhs.offset < rhs.offset
                }
                .map(\.element)
        case .online:
            // "Online" means healthy — online-without-warnings. A server in
            // warn is counted as online in the header summary but users
            // clicking the Online pill mean "show me the ones that are
            // actually fine," so we exclude warn/critical here.
            bySeverity = store.serverVMs.filter { vm in
                if case .online = vm.state { return true }
                return false
            }
        case .warn:
            // Rank critical above warn so the most severe hosts are seen
            // first when the user clicks the warn pill. Tie-break on the
            // original index so registration order holds within a tier.
            bySeverity = store.serverVMs
                .enumerated()
                .filter { _, vm in
                    switch vm.state {
                    case .warn, .critical: return true
                    default: return false
                    }
                }
                .sorted { lhs, rhs in
                    let ra = severityRank(lhs.element.state)
                    let rb = severityRank(rhs.element.state)
                    if ra != rb { return ra > rb }
                    return lhs.offset < rhs.offset
                }
                .map(\.element)
        case .down:
            bySeverity = store.serverVMs.filter { vm in
                if case .offline = vm.state { return true }
                return false
            }
        }

        let afterSearch = applySearch(bySeverity)
        return sortFavoritesFirst(afterSearch)
    }

    private func applySearch(_ vms: [ServerViewModel]) -> [ServerViewModel] {
        let q = debouncedQuery.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return vms }
        return vms.filter { vm in
            let node = nodeStore.node(withId: vm.id)
            if vm.hostname.lowercased().contains(q) { return true }
            if vm.dnsName.lowercased().contains(q) { return true }
            if let n = node {
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
    private func sortFavoritesFirst(_ vms: [ServerViewModel]) -> [ServerViewModel] {
        var favorites: [ServerViewModel] = []
        var rest: [ServerViewModel] = []
        for vm in vms {
            if nodeStore.node(withId: vm.id)?.favorite == true {
                favorites.append(vm)
            } else {
                rest.append(vm)
            }
        }
        return favorites + rest
    }
}
