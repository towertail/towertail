import Foundation

enum FullViewMode: Equatable, Sendable {
    case live
    case paused(Date)
    case pinned(Date)
}

/// DISK tab picker selections. Strings are the sentinel "aggregate"
/// value (nil semantically) or the specific mount/device name.
enum DiskMountSelection: Hashable, Sendable {
    case max                  // worst fill % across all mounts
    case mount(String)        // specific mount path
}

enum DiskDeviceSelection: Hashable, Sendable {
    case total                // sum across all physical devices
    case device(String)       // specific device (e.g. "nvme0n1")
}

@Observable
@MainActor
final class FullViewModel {
    var metric: Metric
    var mode: FullViewMode = .live
    var hoverAt: Date?

    /// DISK tab — capacity chart mount picker. "Max" is the default so
    /// the tab opens with the same headline number users have seen since
    /// v1 (`worstDisk`).
    var diskMount: DiskMountSelection = .max
    /// DISK tab — I/O chart device picker. "Total" aggregates across
    /// devices.
    var diskDevice: DiskDeviceSelection = .total

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
