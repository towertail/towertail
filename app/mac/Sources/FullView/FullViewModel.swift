import Foundation

enum FullViewMode: Equatable, Sendable {
    case live
    case paused(Date)
    case pinned(Date)
}

@Observable
@MainActor
final class FullViewModel {
    var metric: Metric
    var mode: FullViewMode = .live
    var hoverAt: Date?

    init(metric: Metric) {
        self.metric = metric
    }

    var isLive: Bool {
        if case .live = mode { return true }
        return false
    }

    func effectiveTimestamp(latest: Date?) -> Date? {
        if let hoverAt { return hoverAt }
        switch mode {
        case .live: return latest
        case .paused(let t), .pinned(let t): return t
        }
    }

    func togglePlayPause(latest: Date?) {
        switch mode {
        case .live:
            mode = .paused(latest ?? Date())
        case .paused:
            mode = .live
        case .pinned(let t):
            mode = .paused(t)
        }
    }

    func pin(at t: Date) {
        mode = .pinned(t)
    }

    func unpin() {
        if case .pinned = mode {
            mode = .live
        }
    }
}
