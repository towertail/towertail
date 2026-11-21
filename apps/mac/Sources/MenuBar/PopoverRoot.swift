import SwiftUI

enum PopoverFilter: Hashable {
    case all
    case online
    case warn
    case down
}

struct PopoverRoot: View {
    @Environment(ServerStore.self) private var store
    @Environment(NodeStore.self) private var nodeStore
    @Environment(ClientSettings.self) private var clientSettings
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(\.viewLease) private var viewLease
    @Environment(\.backend) private var backend
    /// `MenuBarExtra(style: .window)` keeps its popover content view tree
    /// alive even after the user dismisses the popover (window ordered
    /// out, not destroyed) so the next click feels instant. The downside:
    /// every ServerCardView's 1Hz `TimelineView` keeps firing and every
    /// CanvasSparkline keeps redrawing on each ingest, even with the
    /// popover hidden. We short-circuit on `scenePhase == .background` to
    /// stop reading any @Observable state, which is what schedules those
    /// updates. Reopening the popover restores `.active` and the body
    /// re-runs normally.
    @Environment(\.scenePhase) private var scenePhase
    @State private var filter: PopoverFilter = .all
    /// Live text from the search field. Changes drive a debounce task that
    /// copies into `debouncedQuery` — typing doesn't rebuild the card list
    /// on every keystroke.
    @State private var searchText: String = ""
    @State private var debouncedQuery: String = ""
    @State private var debounceTask: Task<Void, Never>?
    @State private var hover: MetricHoverState

    init(hover: MetricHoverState = MetricHoverState()) {
        _hover = State(initialValue: hover)
    }

    /// Felt-out by typing tests: 250ms is too snappy (each keystroke
    /// rebuilds the list mid-word), 600ms feels laggy on backspace.
    /// 400ms is the sweet spot where a fast typist still sees one rebuild
    /// per word, and a slow typist sees one rebuild between letters.
    private static let searchDebounce: Duration = .milliseconds(400)

    var body: some View {
        // A ZStack, not a Group: a Group applies the modifiers to each branch.
        ZStack {
            if scenePhase == .background {
                // Same fixed size as the active body so AppKit's window
                // measurement doesn't churn when the popover reopens.
                Color.clear.frame(width: 360, height: 620)
            } else {
                activeBody
            }
        }
        // Outside the `if`: the active body is gone while the popover is hidden.
        .onAppear { viewLease?.setPopover(visible: scenePhase != .background) }
        .onChange(of: scenePhase) { _, phase in
            viewLease?.setPopover(visible: phase != .background)
            hover.dismiss()
        }
        .task(id: scenePhase != .background) { await backfillCards() }
    }

    /// Fills the cards of Cloud hosts from REST when the popover shows. The
    /// wait lets the lazy list report its cards first.
    private func backfillCards() async {
        guard scenePhase != .background, let loader = backend?.cloudHistory else { return }
        try? await Task.sleep(for: .milliseconds(300))
        guard !Task.isCancelled, let lease = viewLease else { return }
        let ids = store.serverVMs
            .filter { $0.liveSource == .cloud && lease.visibleCards.contains($0.id) }
            .map(\.id)
        await loader.backfillCards(ids)
    }

    @ViewBuilder
    private var activeBody: some View {
        VStack(spacing: 0) {
            PopoverHeader(filter: $filter, searchText: $searchText)
            Divider().opacity(0.3)
            switch clientSettings.popoverLayout {
            case .cards:
                ScrollView {
                    LazyVStack(spacing: 10) {
                        ForEach(filteredVMs) { vm in
                            ServerCardView(vm: vm)
                                .onAppear { viewLease?.setCard(vm.id, visible: true) }
                                .onDisappear { viewLease?.setCard(vm.id, visible: false) }
                        }
                    }
                    .padding(10)
                }
            case .rows:
                rowsBody
            }
            PopoverFooter(account: cloud?.account, showsLegend: clientSettings.popoverLayout == .rows)
        }
        .frame(width: 360, height: 620)
        .background(Color(nsColor: .windowBackgroundColor))
        .coordinateSpace(.named(MetricHoverState.space))
        .overlay(alignment: .topLeading) { MetricHoverOverlay() }
        .environment(hover)
        .onChange(of: filter) { hover.dismiss() }
        .onChange(of: clientSettings.popoverLayout) { hover.dismiss() }
        .onChange(of: searchText) { _, newValue in
            hover.dismiss()
            debounceTask?.cancel()
            // Empty transitions should apply immediately — hitting the X
            // or Escape feels broken if it lags behind the debounce.
            if newValue.isEmpty {
                debouncedQuery = ""
                return
            }
            debounceTask = Task { @MainActor in
                try? await Task.sleep(for: Self.searchDebounce)
                if Task.isCancelled { return }
                debouncedQuery = newValue
            }
        }
    }

    @ViewBuilder
    private var rowsBody: some View {
        let vms = filteredVMs
        ServerRowsHeader(count: vms.count)
        ScrollView {
            LazyVStack(spacing: 0) {
                ForEach(vms) { vm in
                    Divider().opacity(0.4)
                    ServerRowView(vm: vm)
                        .onAppear { viewLease?.setCard(vm.id, visible: true) }
                        .onDisappear { viewLease?.setCard(vm.id, visible: false) }
                }
            }
        }
    }

    private var filteredVMs: [ServerViewModel] {
        ServerFilter.apply(
            vms: store.serverVMs,
            filter: filter,
            query: debouncedQuery,
            nodeLookup: { [nodeStore] id in nodeStore.node(withId: id) }
        )
    }
}
