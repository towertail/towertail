import Foundation

/// Disk I/O of one process in bytes per second.
struct ProcIORate: Equatable, Sendable {
    let read: Double
    let write: Double
}

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

    struct Snapshot: Sendable, TimeStamped {
        let t: Date
        let items: [ProcSample]
    }

    private var buffer: TimeSeriesBuffer<Snapshot>

    init(retention: TimeInterval = ProcSeries.defaultRetention) {
        self.retention = retention
        self.buffer = TimeSeriesBuffer(
            trim: .timeWindow(retention, hardCap: Self.hardSlotCap),
            reserveCapacity: 256
        )
    }

    var latest: Snapshot? { buffer.latest }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    mutating func append(_ s: Snapshot) {
        buffer.append(s)
    }

    /// Replace the whole buffer with `snapshots` (oldest first). Hydration
    /// on launch uses this instead of calling `append` in a loop — a
    /// 3,600-row replay was O(n²) under the per-append trim and fired one
    /// @Observable invalidation per snapshot.
    mutating func replace(with snapshots: [Snapshot]) {
        buffer.replace(with: snapshots)
    }

    func nearest(to date: Date) -> Snapshot? {
        buffer.nearest(to: date)
    }

    /// The snapshots inside `range`, oldest first.
    func snapshots(in range: ClosedRange<Date>) -> [Snapshot] {
        buffer.snapshot().filter { range.contains($0.t) }
    }

    /// Returns the snapshot immediately preceding `snap` in the buffer,
    /// or nil if `snap` is the first entry. Used for per-process I/O rate
    /// computation (cumulative counter delta divided by dt).
    func previous(before snap: Snapshot) -> Snapshot? {
        // Match by timestamp — snapshots are appended strictly monotonic
        // so equality of `t` uniquely identifies the index.
        guard let idx = buffer.index(matching: snap.t), idx > 0 else { return nil }
        return buffer.element(at: idx - 1)
    }

    /// Mean rates of each PID in the latest snapshot, over the last `n`
    /// snapshots that list the PID with rates. Empty when none has rates.
    func ioRates(lastSamples n: Int = 5) -> [Int32: ProcIORate] {
        guard let latest = buffer.latest else { return [:] }
        let first = max(0, buffer.count - n)
        var sums: [Int32: (read: Double, write: Double, count: Int)] = [:]
        for i in first..<buffer.count {
            guard let snap = buffer.element(at: i) else { continue }
            for p in snap.items {
                guard let r = p.readBps, let w = p.writeBps else { continue }
                var acc = sums[p.pid] ?? (0, 0, 0)
                acc.read += Double(r)
                acc.write += Double(w)
                acc.count += 1
                sums[p.pid] = acc
            }
        }
        var out: [Int32: ProcIORate] = [:]
        for p in latest.items {
            guard let acc = sums[p.pid] else { continue }
            out[p.pid] = ProcIORate(read: acc.read / Double(acc.count), write: acc.write / Double(acc.count))
        }
        return out
    }
}
