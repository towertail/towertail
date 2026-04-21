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
    case .unknown: return -1
    }
}

struct PopoverRoot: View {
    @Environment(ServerStore.self) private var store
    @State private var filter: PopoverFilter = .all

    var body: some View {
        VStack(spacing: 0) {
            PopoverHeader(filter: $filter)
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
    }

    private var filteredVMs: [ServerViewModel] {
        switch filter {
        case .all: return store.serverVMs
        case .online:
            // "Online" means healthy — online-without-warnings. A server in
            // warn is counted as online in the header summary but users
            // clicking the Online pill mean "show me the ones that are
            // actually fine," so we exclude warn/critical here.
            return store.serverVMs.filter { vm in
                if case .online = vm.state { return true }
                return false
            }
        case .warn:
            // Rank critical above warn so the most severe hosts are seen
            // first when the user clicks the warn pill.
            return store.serverVMs
                .filter { vm in
                    switch vm.state {
                    case .warn, .critical: return true
                    default: return false
                    }
                }
                .sorted { a, b in
                    severityRank(a.state) > severityRank(b.state)
                }
        case .down:
            return store.serverVMs.filter { vm in
                if case .offline = vm.state { return true }
                return false
            }
        }
    }
}
