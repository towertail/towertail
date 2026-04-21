import Foundation

/// A ring buffer of `(timestamp, [ProcSample])` snapshots. The full-view
/// process table uses this to look up which procs were running at the
/// user's hovered timestamp on the timeline.
///
/// Procs are expensive to keep — 20 processes × ~100 bytes × 720 slots
/// ≈ 1.4 MB per host. We cap at a smaller capacity than MetricSeries
/// because historical proc tables are only useful for recent hover and
/// rarely retained for the full 6h window.
struct ProcSeries: Sendable {
    static let capacity = 240

    struct Snapshot: Sendable {
        let t: Date
        let items: [ProcSample]
    }

    private var buffer: ContiguousArray<Snapshot>

    init() {
        buffer = ContiguousArray<Snapshot>()
        buffer.reserveCapacity(Self.capacity)
    }

    var latest: Snapshot? { buffer.last }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    mutating func append(_ s: Snapshot) {
        if buffer.count == Self.capacity {
            buffer.removeFirst()
        }
        buffer.append(s)
    }

    /// Binary-search for the snapshot nearest the given timestamp.
    func nearest(to date: Date) -> Snapshot? {
        guard !buffer.isEmpty else { return nil }
        var lo = 0
        var hi = buffer.count - 1
        while lo < hi {
            let mid = (lo + hi) / 2
            if buffer[mid].t < date { lo = mid + 1 } else { hi = mid }
        }
        if lo == 0 { return buffer[0] }
        let a = buffer[lo - 1]
        let b = buffer[lo]
        return abs(a.t.timeIntervalSince(date)) <= abs(b.t.timeIntervalSince(date)) ? a : b
    }
}
