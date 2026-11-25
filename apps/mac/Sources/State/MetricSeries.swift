import Foundation
import SwiftUI

struct MetricPoint: Identifiable, Equatable, Sendable, TimeStamped {
    let t: Date
    let v: Double
    var id: Date { t }
}

/// The points of one time window with their peak and average.
struct MetricWindow: Equatable, Sendable {
    let points: [MetricPoint]
    let peak: Double
    let average: Double
}

/// Fixed-capacity ring of `(timestamp, value)` samples. Card sparklines
/// and the full-view charts both read from this. Backed by the generic
/// `TimeSeriesBuffer` — keep the public surface stable so callers and
/// tests that touch `.snapshot()`, `.nearest(to:)`, `.tint(...)` don't
/// need to change.
struct MetricSeries: Sendable {
    static let capacity = 720

    private var buffer: TimeSeriesBuffer<MetricPoint>

    init() {
        buffer = TimeSeriesBuffer(trim: .maxCount(Self.capacity), reserveCapacity: Self.capacity)
    }

    var latest: MetricPoint? { buffer.latest }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    mutating func append(_ p: MetricPoint) {
        buffer.append(p)
    }

    /// Puts `points` (oldest first) before the live points. Drops the
    /// points at or after the oldest live point. Trims to `capacity`.
    mutating func merge(older points: [MetricPoint]) {
        let live = buffer.snapshot()
        let older = live.first.map { first in points.filter { $0.t < first.t } } ?? points
        buffer.replace(with: older + live)
    }

    /// Takes REST history. It replaces the points in the range of `points`.
    /// Older points (for example SSH data before a move to Cloud) and newer
    /// live points stay.
    mutating func merge(history points: [MetricPoint]) {
        guard let first = points.first, let last = points.last else { return }
        let current = buffer.snapshot()
        let older = current.filter { $0.t < first.t }
        let newer = current.filter { $0.t > last.t }
        buffer.replace(with: older + points + newer)
    }

    func snapshot() -> [MetricPoint] {
        buffer.snapshot()
    }

    /// The last `seconds` before the latest point. Nil when empty.
    /// `capacity` holds 15 minutes only when the poll is 1.25 s or longer.
    func window(_ seconds: TimeInterval) -> MetricWindow? {
        guard let latest else { return nil }
        let from = latest.t.addingTimeInterval(-seconds)
        let points = buffer.snapshot().filter { $0.t >= from }
        let values = points.map(\.v)
        return MetricWindow(
            points: points,
            peak: values.max() ?? latest.v,
            average: values.reduce(0, +) / Double(max(values.count, 1))
        )
    }

    func tint(warn: Double, critical: Double) -> ThresholdTint {
        guard let v = latest?.v else { return .stale }
        if v >= critical { return .critical }
        if v >= warn { return .warn }
        return .nominal
    }

    func nearest(to date: Date) -> MetricPoint? {
        buffer.nearest(to: date)
    }
}
