import SwiftUI

struct MetricHoverTarget: Equatable {
    let hostId: UUID
    let metric: Metric
    /// Frame of the metric column in `MetricHoverState.space`.
    let anchor: CGRect

    func isSameSlot(as other: MetricHoverTarget) -> Bool {
        hostId == other.hostId && metric == other.metric
    }
}

/// Which metric card shows. A card shows after a short hover, not at once.
@Observable @MainActor
final class MetricHoverState {
    static let space = "popover"
    static let delay: Duration = .milliseconds(350)

    private(set) var shown: MetricHoverTarget?
    @ObservationIgnored private let delay: Duration
    @ObservationIgnored private var pending: MetricHoverTarget?
    @ObservationIgnored private var task: Task<Void, Never>?

    init(delay: Duration = MetricHoverState.delay) {
        self.delay = delay
    }

    func enter(_ target: MetricHoverTarget) {
        cancelPending()
        if shown?.isSameSlot(as: target) == true { return }
        pending = target
        task = Task { [delay] in
            try? await Task.sleep(for: delay)
            guard !Task.isCancelled else { return }
            pending = nil
            shown = target
        }
    }

    func exit(_ target: MetricHoverTarget) {
        if pending?.isSameSlot(as: target) == true { cancelPending() }
        if shown?.isSameSlot(as: target) == true { shown = nil }
    }

    func show(_ target: MetricHoverTarget) {
        cancelPending()
        shown = target
    }

    func dismiss() {
        cancelPending()
        shown = nil
    }

    private func cancelPending() {
        task?.cancel()
        task = nil
        pending = nil
    }
}

/// Draws the card of `MetricHoverState.shown` above the popover content.
struct MetricHoverOverlay: View {
    private static let margin: CGFloat = 8
    private static let gap: CGFloat = 6

    @Environment(MetricHoverState.self) private var hover
    @Environment(ServerStore.self) private var store
    @State private var cardSize: CGSize = .zero

    /// Top-left corner of the card: below the anchor, above it when it does not fit.
    static func origin(anchor: CGRect, card: CGSize, bounds: CGSize) -> CGPoint {
        let maxX = max(margin, bounds.width - margin - card.width)
        let x = min(max(anchor.midX - card.width / 2, margin), maxX)
        let below = anchor.maxY + gap
        let y = below + card.height <= bounds.height - margin
            ? below
            : max(margin, anchor.minY - gap - card.height)
        return CGPoint(x: x, y: y)
    }

    var body: some View {
        GeometryReader { g in
            if let target = hover.shown,
               let vm = store.serverVMs.first(where: { $0.id == target.hostId }),
               let content = MetricHoverContent.make(vm: vm, metric: target.metric) {
                let origin = Self.origin(anchor: target.anchor, card: cardSize, bounds: g.size)
                MetricHoverCard(content: content)
                    .onGeometryChange(for: CGSize.self, of: \.size) { cardSize = $0 }
                    .opacity(cardSize == .zero ? 0 : 1)
                    .offset(x: origin.x, y: origin.y)
            }
        }
        .allowsHitTesting(false)
    }
}
