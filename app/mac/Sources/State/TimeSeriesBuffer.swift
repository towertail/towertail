import Foundation

/// Anything that lives at a single point in time. The buffer needs this
/// only for `nearest(to:)` and time-based trimming — payload shape is
/// otherwise opaque.
protocol TimeStamped {
    var t: Date { get }
}

/// How a `TimeSeriesBuffer` decides which entries to drop after an
/// append (or a bulk replace). Hidden behind an enum rather than a
/// closure so the policy is `Sendable` for free and survives storing the
/// buffer in `@Observable` value-typed state.
enum TimeSeriesTrim: Sendable, Equatable {
    /// Hard cap on element count. Oldest entries drop first. The original
    /// `MetricSeries` ring buffer (capacity 720) maps to this.
    case maxCount(Int)
    /// Drop entries older than `retention` measured *relative to the
    /// newest entry's timestamp* (not wall-clock `Date()`). The slot cap
    /// is a defensive ceiling for sub-second pollers — the original
    /// `ProcSeries` (2h retention, 8 192 slot cap) maps to this.
    case timeWindow(TimeInterval, hardCap: Int)
}

/// Append-with-trim time series, generic over the element payload.
/// Backs `MetricSeries` and `ProcSeries`; both used to re-implement the
/// same `ContiguousArray` + binary-search + trim dance separately.
///
/// **Mutation semantics.** Value type, so it composes inside other value
/// types and `@Observable` classes pick up changes via assignment. Not
/// thread-safe — callers serialise on `@MainActor` like the rest of the
/// state layer.
struct TimeSeriesBuffer<Element: TimeStamped & Sendable>: Sendable {
    private(set) var trim: TimeSeriesTrim
    private var buffer: ContiguousArray<Element>

    init(trim: TimeSeriesTrim, reserveCapacity: Int = 0) {
        self.trim = trim
        self.buffer = ContiguousArray<Element>()
        if reserveCapacity > 0 {
            self.buffer.reserveCapacity(reserveCapacity)
        }
    }

    var latest: Element? { buffer.last }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    func snapshot() -> [Element] { Array(buffer) }

    mutating func append(_ element: Element) {
        buffer.append(element)
        applyTrim(referenceNewest: element.t)
    }

    /// Replace the whole buffer in one shot, then apply trim once.
    /// Hydration from SQLite uses this — appending in a loop was O(n²)
    /// under the per-append trim and fired one observation per row.
    mutating func replace(with elements: [Element]) {
        var buf = ContiguousArray<Element>()
        buf.reserveCapacity(elements.count)
        buf.append(contentsOf: elements)
        buffer = buf
        if let newest = buffer.last?.t {
            applyTrim(referenceNewest: newest)
        }
    }

    private mutating func applyTrim(referenceNewest newest: Date) {
        switch trim {
        case .maxCount(let cap):
            if buffer.count > cap {
                buffer.removeFirst(buffer.count - cap)
            }
        case .timeWindow(let retention, let hardCap):
            let cutoff = newest.addingTimeInterval(-retention)
            var drop = 0
            while drop < buffer.count && buffer[drop].t < cutoff {
                drop += 1
            }
            if drop > 0 {
                buffer.removeFirst(drop)
            }
            if buffer.count > hardCap {
                buffer.removeFirst(buffer.count - hardCap)
            }
        }
    }

    /// Binary-search for the entry nearest the given timestamp. Returns
    /// nil only when the buffer is empty — callers outside the time
    /// range get the closest endpoint.
    func nearest(to date: Date) -> Element? {
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

    /// Index lookup for the entry whose timestamp exactly matches `t`,
    /// or nil if none. Used by `ProcSeries.previous(before:)` for rate
    /// math — exact equality is fine because snapshot timestamps are
    /// strictly monotonic.
    func index(matching t: Date) -> Int? {
        buffer.firstIndex(where: { $0.t == t })
    }

    /// Element at a raw index. `nil` for out-of-range — keeps callers
    /// from open-coding bounds checks against `buffer`.
    func element(at index: Int) -> Element? {
        guard buffer.indices.contains(index) else { return nil }
        return buffer[index]
    }
}
