import Foundation
import SwiftUI

struct MetricPoint: Identifiable, Equatable, Sendable {
    let t: Date
    let v: Double
    var id: Date { t }
}

struct MetricSeries: Sendable {
    static let capacity = 720

    private var buffer: ContiguousArray<MetricPoint>

    init() {
        buffer = ContiguousArray<MetricPoint>()
        buffer.reserveCapacity(Self.capacity)
    }

    var latest: MetricPoint? { buffer.last }
    var count: Int { buffer.count }
    var isEmpty: Bool { buffer.isEmpty }

    mutating func append(_ p: MetricPoint) {
        if buffer.count == Self.capacity {
            buffer.removeFirst()
        }
        buffer.append(p)
    }

    func snapshot() -> [MetricPoint] {
        Array(buffer)
    }

    func tint(warn: Double, critical: Double) -> ThresholdTint {
        guard let v = latest?.v else { return .stale }
        if v >= critical { return .critical }
        if v >= warn { return .warn }
        return .nominal
    }

    func nearest(to date: Date) -> MetricPoint? {
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
