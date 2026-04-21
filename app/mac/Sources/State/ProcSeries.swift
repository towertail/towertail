import Foundation

/// A time-windowed buffer of `(timestamp, [ProcSample])` snapshots. The
/// full-view process table uses this to look up which procs were running
/// at the user's hovered timestamp on the timeline.
///
/// Retention is **time-based**, not slot-based: we keep the last
/// `retention` of snapshots regardless of polling rate. A slot-based cap
/// gave wildly different windows on local (2s poll → 8 min) vs. SSH (10s
/// poll → 40 min) hosts for the same 240 slots.
///
/// Procs are expensive: 20 processes × ~100 B × ~3600 snapshots (2h at
/// 2s) ≈ 7 MB per host. A safety slot cap guards against runaway growth
/// if a host polls at sub-second rates.
struct ProcSeries: Sendable {
    static let defaultRetention: TimeInterval = 2 * 60 * 60 // 2 hours
    static let hardSlotCap = 8_192 // ~2.25h at 1s; ~4.5h at 2s. Defense in depth.

    let retention: TimeInterval

    struct Snapshot: Sendable {
        let t: Date
        let items: [ProcSample]
    }

    private var buffer: ContiguousArray<Snapshot>

    init(retention: TimeInterval = ProcSeries.defaultRetention) {
        self.retention = retention
        buffer = ContiguousArray<Snapshot>()
        buffer.reserveCapacity(256)
    }

    var latest: Snapshot? { buffer.last }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    mutating func append(_ s: Snapshot) {
        buffer.append(s)
        // Trim snapshots older than the retention window, measured
        // relative to the newest snapshot (not wall-clock Date()): when
        // the user pauses polling on a host, we want the existing window
        // preserved rather than walked forward by real time.
        let cutoff = s.t.addingTimeInterval(-retention)
        var drop = 0
        while drop < buffer.count && buffer[drop].t < cutoff {
            drop += 1
        }
        if drop > 0 {
            buffer.removeFirst(drop)
        }
        // Safety: cap absolute count so a misconfigured sub-second poller
        // can't grow the buffer unboundedly.
        if buffer.count > Self.hardSlotCap {
            buffer.removeFirst(buffer.count - Self.hardSlotCap)
        }
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
