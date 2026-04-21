import XCTest
@testable import Towertail

final class ProcSeriesTests: XCTestCase {
    private func snap(at t: Date, count: Int = 3) -> ProcSeries.Snapshot {
        let items = (0..<count).map { i in
            ProcSample(pid: Int32(i), name: "p\(i)", cpuPct: 0, rss: 0)
        }
        return ProcSeries.Snapshot(t: t, items: items)
    }

    func testRetainsFullWindowAtShortPollInterval() {
        // Before the fix: 240 slots × 2s = 8 minutes. Now: 2h regardless.
        var s = ProcSeries(retention: 2 * 3600)
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        // 2700 snapshots at 2s apart = 90 minutes — comfortably inside
        // the 2h window; none should be trimmed.
        for i in 0..<2700 {
            s.append(snap(at: t0.addingTimeInterval(Double(i * 2))))
        }
        XCTAssertEqual(s.count, 2700)
        // The oldest entry is 90m old relative to the newest — verify
        // we can still look it up.
        let oldest = s.nearest(to: t0)
        XCTAssertEqual(oldest?.t, t0)
    }

    func testTrimsSnapshotsOlderThanRetention() {
        var s = ProcSeries(retention: 60) // 1 minute window for fast test
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<120 { // 120 snapshots × 1s apart = 2 minutes
            s.append(snap(at: t0.addingTimeInterval(Double(i))))
        }
        // Newest is at t0+119. Retention 60s → oldest kept should be ≥ t0+59.
        XCTAssertEqual(s.latest?.t, t0.addingTimeInterval(119))
        let oldestKept = s.nearest(to: t0)
        XCTAssertGreaterThanOrEqual(
            oldestKept!.t.timeIntervalSince(t0),
            59,
            "snapshots older than the retention window should be trimmed"
        )
    }

    func testHardSlotCapPreventsUnboundedGrowth() {
        // Sub-second polling with an absurdly long retention shouldn't
        // let the buffer grow past the hard cap.
        var s = ProcSeries(retention: 10 * 3600)
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<(ProcSeries.hardSlotCap + 500) {
            s.append(snap(at: t0.addingTimeInterval(Double(i) * 0.1)))
        }
        XCTAssertLessThanOrEqual(s.count, ProcSeries.hardSlotCap)
    }

    func testNearestBinarySearch() {
        var s = ProcSeries()
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<10 {
            s.append(snap(at: t0.addingTimeInterval(Double(i * 10))))
        }
        XCTAssertEqual(s.nearest(to: t0.addingTimeInterval(32))?.t, t0.addingTimeInterval(30))
        XCTAssertEqual(s.nearest(to: t0.addingTimeInterval(-100))?.t, t0)
        XCTAssertEqual(s.nearest(to: t0.addingTimeInterval(10_000))?.t, t0.addingTimeInterval(90))
    }
}
