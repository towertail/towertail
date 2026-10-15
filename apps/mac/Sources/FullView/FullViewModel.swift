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

/// NET tab — picks which secondary table sits under the chart. Defaults
/// to processes (parity with every other tab); `ports` flips to the
/// per-process socket footprint table.
enum NetSubTab: String, CaseIterable, Identifiable, Sendable {
    case processes
    case ports
    var id: String { rawValue }
    var displayName: String {
        switch self {
        case .processes: return "Processes"
        case .ports: return "Ports"
        }
    }
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

    /// NET tab — picks Processes or Ports for the table below the chart.
    /// Persisted only in memory; resets to `.processes` each time the
    /// window opens so the default state is the familiar one.
    var netSubTab: NetSubTab = .processes

    /// Committed zoom range. When set, charts render only samples within
    /// it and the x-axis is bounded to this window. Stacking zoom levels
    /// (each "Zoom in" replaces the current range with the user's new,
    /// narrower selection) falls out naturally — we only need to remember
    /// the *current* window, not the stack of previous ones.
    var zoomRange: ClosedRange<Date>?

    /// Uncommitted drag selection on the chart. Drives the highlight
    /// rectangle and, while non-nil, the Zoom-in button. Cleared when
    /// the user zooms, resets, or clicks outside the selection.
    var selectionStart: Date?
    var selectionEnd: Date?

    init(metric: Metric) {
        self.metric = metric
    }

    /// Ordered (start ≤ end) view of the live drag selection. `nil` when
    /// the user hasn't dragged anything or the drag was zero-width.
    var pendingSelection: ClosedRange<Date>? {
        guard let s = selectionStart, let e = selectionEnd else { return nil }
        if s == e { return nil }
        return s < e ? s...e : e...s
    }

    var canZoomIn: Bool { pendingSelection != nil }
    var canResetZoom: Bool { zoomRange != nil }

    func beginSelection(at t: Date) {
        selectionStart = t
        selectionEnd = t
    }

    func updateSelection(to t: Date) {
        selectionEnd = t
    }

    func clearSelection() {
        selectionStart = nil
        selectionEnd = nil
    }

    /// Commits the current drag as the new zoom window. Selection is
    /// cleared afterwards so the user can immediately drag again to zoom
    /// further.
    func zoomInToSelection() {
        guard let range = pendingSelection else { return }
        zoomRange = range
        clearSelection()
    }

    func resetZoom() {
        zoomRange = nil
        clearSelection()
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
