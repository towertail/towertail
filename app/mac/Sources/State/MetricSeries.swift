import Foundation
import SwiftUI

struct MetricPoint: Identifiable, Equatable, Sendable, TimeStamped {
    let t: Date
    let v: Double
    var id: Date { t }
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

    func snapshot() -> [MetricPoint] {
        buffer.snapshot()
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
