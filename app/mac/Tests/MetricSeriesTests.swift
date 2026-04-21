import XCTest
@testable import Towertail

final class MetricSeriesTests: XCTestCase {
    func testRingBufferWrapsAtCapacity() {
        var s = MetricSeries()
        let base = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<(MetricSeries.capacity + 50) {
            s.append(MetricPoint(t: base.addingTimeInterval(Double(i)), v: Double(i) / 1000.0))
        }
        XCTAssertEqual(s.count, MetricSeries.capacity)
        let snap = s.snapshot()
        XCTAssertEqual(snap.first?.t, base.addingTimeInterval(50))
        XCTAssertEqual(snap.last?.t, base.addingTimeInterval(Double(MetricSeries.capacity + 50 - 1)))
    }

    func testNearestExactMatch() {
        var s = MetricSeries()
        let t0 = Date(timeIntervalSince1970: 1_000)
        for i in 0..<10 {
            s.append(MetricPoint(t: t0.addingTimeInterval(Double(i * 10)), v: Double(i)))
        }
        let target = t0.addingTimeInterval(30)
        let n = s.nearest(to: target)
        XCTAssertEqual(n?.t, target)
        XCTAssertEqual(n?.v, 3)
    }

    func testNearestInterpolated() {
        var s = MetricSeries()
        let t0 = Date(timeIntervalSince1970: 1_000)
        s.append(MetricPoint(t: t0, v: 0))
        s.append(MetricPoint(t: t0.addingTimeInterval(10), v: 1))
        let mid = t0.addingTimeInterval(4)
        XCTAssertEqual(s.nearest(to: mid)?.v, 0)
        let mid2 = t0.addingTimeInterval(6)
        XCTAssertEqual(s.nearest(to: mid2)?.v, 1)
    }

    func testTintBoundaries() {
        var s = MetricSeries()
        s.append(MetricPoint(t: Date(), v: 0.5))
        XCTAssertEqual(s.tint(warn: 0.75, critical: 0.9), .nominal)

        s.append(MetricPoint(t: Date(), v: 0.75))
        XCTAssertEqual(s.tint(warn: 0.75, critical: 0.9), .warn, "warn boundary should be inclusive (>=)")

        s.append(MetricPoint(t: Date(), v: 0.9))
        XCTAssertEqual(s.tint(warn: 0.75, critical: 0.9), .critical, "critical boundary should be inclusive (>=)")

        s.append(MetricPoint(t: Date(), v: 0.95))
        XCTAssertEqual(s.tint(warn: 0.75, critical: 0.9), .critical)
    }

    func testEmptySeriesTintIsStale() {
        let s = MetricSeries()
        XCTAssertEqual(s.tint(warn: 0.5, critical: 0.8), .stale)
    }
}
