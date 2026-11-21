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

    private func points(_ times: [Double], value: Double = 0) -> [MetricPoint] {
        times.map { MetricPoint(t: Date(timeIntervalSince1970: $0), v: value) }
    }

    func testMergeOlderKeepsLivePoints() {
        var s = MetricSeries()
        for p in points([100, 110], value: 9) { s.append(p) }
        s.merge(older: points(Array(stride(from: 50.0, through: 105.0, by: 5)), value: 1))
        let snap = s.snapshot()
        XCTAssertEqual(snap.map { $0.t.timeIntervalSince1970 }, Array(stride(from: 50.0, through: 95.0, by: 5)) + [100, 110])
        XCTAssertEqual(snap.suffix(2).map(\.v), [9, 9])
    }

    func testMergeOlderTrimsToCapacity() {
        var s = MetricSeries()
        s.merge(older: points((0..<800).map(Double.init)))
        XCTAssertEqual(s.count, MetricSeries.capacity)
        XCTAssertEqual(s.latest?.t.timeIntervalSince1970, 799)
    }

    func testMergeHistoryReplacesLivePointsUpToLastRestPoint() {
        var s = MetricSeries()
        for p in points([10, 100, 110, 120], value: 9) { s.append(p) }
        s.merge(history: points([50, 100, 110], value: 1))
        XCTAssertEqual(s.snapshot().map { $0.t.timeIntervalSince1970 }, [50, 100, 110, 120])
        XCTAssertEqual(s.snapshot().map(\.v), [1, 1, 1, 9])
    }

    func testWindowPeakAndAverage() throws {
        var s = MetricSeries()
        let t0 = Date(timeIntervalSince1970: 1_000)
        for (i, v) in [0.2, 0.6, 0.4].enumerated() {
            s.append(MetricPoint(t: t0.addingTimeInterval(Double(i)), v: v))
        }
        let w = try XCTUnwrap(s.window(900))
        XCTAssertEqual(w.points.count, 3)
        XCTAssertEqual(w.peak, 0.6, accuracy: 1e-9)
        XCTAssertEqual(w.average, 0.4, accuracy: 1e-9)
    }

    func testWindowKeepsLastSeconds() throws {
        var s = MetricSeries()
        let t0 = Date(timeIntervalSince1970: 10_000)
        s.append(MetricPoint(t: t0, v: 0.9))
        s.append(MetricPoint(t: t0.addingTimeInterval(16 * 60), v: 0.1))
        let w = try XCTUnwrap(s.window(900))
        XCTAssertEqual(w.points.map(\.v), [0.1])
        XCTAssertEqual(w.peak, 0.1, accuracy: 1e-9)
    }

    func testWindowNilWhenEmpty() {
        XCTAssertNil(MetricSeries().window(900))
    }
}
