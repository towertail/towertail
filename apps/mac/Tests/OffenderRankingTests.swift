import XCTest
@testable import Towertail

final class OffenderRankingTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_700_000_000)

    private func proc(_ pid: Int32, _ name: String, cpu: Double, cmd: String? = nil, rss: Int64 = 1_000) -> ProcSample {
        ProcSample(pid: pid, name: name, cmd: cmd, user: "u", cpuPct: cpu, rss: rss, threads: 1, startTS: nil)
    }

    /// One snapshot each 60 s over 1 h.
    private func snaps(_ items: (Int) -> [ProcSample]) -> [ProcSeries.Snapshot] {
        (0..<60).map { ProcSeries.Snapshot(t: t0.addingTimeInterval(Double($0) * 60), items: items($0)) }
    }

    private var window: ClosedRange<Date> { t0...t0.addingTimeInterval(3_600) }

    func testRanksByAverageOverWindow() {
        // "burst" peaks at 100 % for 1 snapshot. "steady" holds 30 % all the time.
        let s = snaps { i in [proc(1, "burst", cpu: i == 5 ? 100 : 0), proc(2, "steady", cpu: 30)] }
        let top = OffenderRanking(snapshots: s, window: window, by: .cpu).top(10)
        XCTAssertEqual(top.map(\.ref.name), ["steady", "burst"])
        XCTAssertEqual(top[0].avg, 30, accuracy: 0.001)
        XCTAssertEqual(top[1].peak, 100, accuracy: 0.001)
    }

    func testGroupsPidsOfSameCommand() {
        let s = snaps { _ in [proc(10, "worker", cpu: 10, cmd: "worker --a"), proc(11, "worker", cpu: 15, cmd: "worker --a"),
                              proc(12, "worker", cpu: 40, cmd: "worker --b")] }
        let top = OffenderRanking(snapshots: s, window: window, by: .cpu).top(10)
        XCTAssertEqual(top.count, 2)
        XCTAssertEqual(top[0].ref.cmd, "worker --b")
        XCTAssertEqual(top[1].avg, 25, accuracy: 0.001)
    }

    func testSeriesHas60Points() {
        let s = snaps { _ in [proc(1, "a", cpu: 10)] }
        let top = OffenderRanking(snapshots: s, window: window, by: .cpu).top(1)
        XCTAssertEqual(top.first?.series.count, 60)
    }

    func testHighForCountsTimeAtOrAbove50Percent() {
        // High in 10 of 60 snapshots, each 60 s apart.
        let s = snaps { i in [proc(1, "a", cpu: i < 10 ? 50 : 49.9)] }
        let top = OffenderRanking(snapshots: s, window: window, by: .cpu).top(1)
        XCTAssertEqual(top.first?.highFor ?? 0, 600, accuracy: 0.001)
    }

    func testSortsByMemoryPeak() {
        let s = snaps { _ in [proc(1, "small", cpu: 90, rss: 10), proc(2, "big", cpu: 1, rss: 9_000)] }
        let top = OffenderRanking(snapshots: s, window: window, by: .rss).top(2)
        XCTAssertEqual(top.map(\.ref.name), ["big", "small"])
    }
}
