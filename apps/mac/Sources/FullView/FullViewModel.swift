import Foundation

enum FullViewMode: Equatable, Sendable {
    case live
    case pinned(Date)
}

/// The six tabs of the host workbench.
enum HostTab: String, CaseIterable, Identifiable, Sendable {
    case overview, processes, health, disks, alerts, settings

    var id: String { rawValue }
    var displayName: String { rawValue.uppercased() }
}

/// The metrics of the rail. The chart of the Overview tab shows the selected one.
enum RailMetric: String, CaseIterable, Identifiable, Sendable {
    case cpu, mem, load, disk, net, io, health

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .cpu: return "CPU"
        case .mem: return "Memory"
        case .load: return "Load"
        case .disk: return "Disk"
        case .net: return "Network"
        case .io: return "Disk I/O"
        case .health: return "Health"
        }
    }

    /// The process sort that fits the metric.
    var procSort: ProcSort {
        switch self {
        case .mem: return .rss
        case .disk, .io: return .io
        case .cpu, .load, .net, .health: return .cpu
        }
    }
}

/// The raw value is the `by=` value of the server.
enum ProcSort: String, CaseIterable, Identifiable, Sendable {
    case cpu
    case rss = "mem"
    case io

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .cpu: return "CPU"
        case .rss: return "Memory"
        case .io: return "I/O"
        }
    }
}

/// The window length of the charts.
enum HostRange: String, CaseIterable, Identifiable, Sendable {
    case h1 = "1h", h6 = "6h", h24 = "24h", d7 = "7d", d30 = "30d"

    var id: String { rawValue }

    var seconds: TimeInterval {
        switch self {
        case .h1: return 3_600
        case .h6: return 6 * 3_600
        case .h24: return 24 * 3_600
        case .d7: return 7 * 24 * 3_600
        case .d30: return 30 * 24 * 3_600
        }
    }

    /// The bucket of the history tier. About 300 points fill the window.
    var step: Duration {
        switch self {
        case .h1: return .seconds(10)
        case .h6: return .seconds(60)
        case .h24: return .seconds(300)
        case .d7: return .seconds(1_800)
        case .d30: return .seconds(7_200)
        }
    }

    var previous: HostRange? {
        Self.allCases.firstIndex(of: self).flatMap { $0 > 0 ? Self.allCases[$0 - 1] : nil }
    }
}

/// The Processes tab: the offenders table, or the listening ports.
enum ProcessesView: String, CaseIterable, Identifiable, Sendable {
    case offenders, ports

    var id: String { rawValue }
    var displayName: String { self == .offenders ? "Offenders" : "Ports" }
}

/// What the process snapshot of the workbench follows.
enum CursorState: Sendable {
    case live, hover, pinned

    @MainActor
    init(model: FullViewModel) {
        if model.hoverAt != nil {
            self = .hover
        } else if case .pinned = model.mode {
            self = .pinned
        } else {
            self = .live
        }
    }
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

/// DISK tab: the charts, or the folder scan with cleanup ideas.
enum DiskSubTab: String, CaseIterable, Identifiable, Sendable {
    case capacity
    case usage
    var id: String { rawValue }
    var displayName: String {
        switch self {
        case .capacity: return "Capacity & I/O"
        case .usage: return "Usage"
        }
    }
}

@Observable
@MainActor
final class FullViewModel {
    var tab: HostTab
    var processesView: ProcessesView = .offenders
    var mode: FullViewMode = .live
    var hoverAt: Date?
    /// Rows selected in the process table.
    var selectedPids: Set<Int32> = []
    /// The process of the open panel.
    var openProcess: ProcessRef?
    /// The process that the Processes tab draws next to the host.
    var offenderSelected: ProcessRef?

    // `range` and `rail` are computed so that a change can clear other state.
    private var storedRange: HostRange
    private var storedRail: RailMetric
    var procSortOverride: ProcSort?

    var range: HostRange {
        get { storedRange }
        set {
            storedRange = newValue
            zoomRange = nil
            mode = .live
            hoverAt = nil
            clearSelection()
        }
    }

    var rail: RailMetric {
        get { storedRail }
        set {
            storedRail = newValue
            procSortOverride = nil
        }
    }

    var procSort: ProcSort { procSortOverride ?? rail.procSort }

    /// DISK tab: capacity chart mount picker. "Max" is the worst mount.
    var diskMount: DiskMountSelection = .max
    /// DISK tab: I/O chart device picker. "Total" adds all devices.
    var diskDevice: DiskDeviceSelection = .total
    /// DISK tab: Capacity & I/O charts or the Usage scan.
    var diskSubTab: DiskSubTab = .capacity

    /// The detail that Cloud sends for the open tab. The Usage tab has none:
    /// disk scans use their own route.
    var detailTab: DetailTab? {
        switch tab {
        case .overview, .alerts, .settings: return .processes
        case .processes: return processesView == .ports ? .ports : .processes
        case .health: return .health
        case .disks: return diskSubTab == .usage ? nil : .disks
        }
    }

    /// One Usage model per node, so that a scan continues when the
    /// window shows another host.
    @ObservationIgnored private var usageModels: [UUID: DiskUsageModel] = [:]

    func usageModel(for node: Node, backend: any Backend, defaultScope: String) -> DiskUsageModel {
        if let m = usageModels[node.id] { return m }
        let m = DiskUsageModel(
            node: node, store: backend.diskScans,
            scanner: backend.diskScanner(for: node), scope: defaultScope
        )
        usageModels[node.id] = m
        return m
    }

    /// Committed zoom range. When set, the charts show only this window.
    /// A new zoom replaces the current one.
    var zoomRange: ClosedRange<Date>?

    /// Uncommitted drag selection on the chart.
    var selectionStart: Date?
    var selectionEnd: Date?

    /// Shorter drags do not zoom.
    static let minZoomSpan: TimeInterval = 120

    init(context: FullViewContext, source: LiveSource) {
        let (tab, rail): (HostTab, RailMetric)
        switch context.metric {
        case .disk: (tab, rail) = (.disks, .disk)
        case .health: (tab, rail) = (.health, .health)
        case .cpu: (tab, rail) = (.overview, .cpu)
        case .mem: (tab, rail) = (.overview, .mem)
        case .net: (tab, rail) = (.overview, .net)
        }
        self.tab = tab
        storedRail = rail
        storedRange = source == .cloud ? .h24 : .h1
    }

    /// The window of the charts: the zoom, or the range that ends now.
    func window(now: Date) -> ClosedRange<Date> {
        zoomRange ?? now.addingTimeInterval(-range.seconds)...now
    }

    /// True when the range shows more than the smaller range before it.
    /// `oldest` is the oldest time that the host has data for.
    func isAvailable(_ r: HostRange, oldest: Date?, now: Date) -> Bool {
        guard let prev = r.previous else { return true }
        guard let oldest else { return false }
        return oldest <= now.addingTimeInterval(-prev.seconds)
    }

    /// Ordered (start <= end) view of the live drag selection. `nil` when
    /// the user has not dragged or the drag has no width.
    var pendingSelection: ClosedRange<Date>? {
        guard let s = selectionStart, let e = selectionEnd else { return nil }
        if s == e { return nil }
        return s < e ? s...e : e...s
    }

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

    /// Ends a drag. A span of 120 s or more becomes the zoom window.
    func endSelection() {
        if let r = pendingSelection, r.upperBound.timeIntervalSince(r.lowerBound) >= Self.minZoomSpan {
            zoomRange = r
        }
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
        case .pinned(let t): return t
        }
    }

    /// Space pins the newest time, or goes back to live when pinned.
    func pinLatest(_ latest: Date?) {
        switch mode {
        case .live:
            if let latest { mode = .pinned(latest) }
        case .pinned:
            mode = .live
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

    /// Esc closes the process panel first, then the pin.
    /// Returns false when it did nothing.
    func handleEscape() -> Bool {
        if openProcess != nil {
            openProcess = nil
            return true
        }
        guard case .pinned = mode else { return false }
        mode = .live
        return true
    }

    /// Moves the pin by `direction * span`, inside the window and not after
    /// `now`. Returns false when the model is live.
    func stepPin(by direction: Int, span: TimeInterval, now: Date) -> Bool {
        guard case .pinned(let t) = mode else { return false }
        let w = window(now: now)
        let upper = min(w.upperBound, now)
        let moved = t.addingTimeInterval(Double(direction) * span)
        mode = .pinned(min(max(moved, w.lowerBound), upper))
        return true
    }

    /// Shows the process next to the host in the Processes tab.
    func compare(_ ref: ProcessRef) {
        tab = .processes
        offenderSelected = ref
    }
}
