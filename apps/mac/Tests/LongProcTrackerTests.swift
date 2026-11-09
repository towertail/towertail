import XCTest
@testable import Towertail

final class LongProcTrackerTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)
    private let started = Date(timeIntervalSince1970: 1_799_000_000)
    private let rule = LongProcRule(enabled: true, cpuPct: 50, sustainSeconds: 1800, notify: .all, ignore: [])

    private func proc(_ cpu: Double, pid: Int32 = 812, name: String = "OrbStack Helper", start: Date? = nil) -> ProcSample {
        ProcSample(pid: pid, name: name, cpuPct: cpu, rss: 1, startTS: start ?? started)
    }

    /// Feeds one sample each minute from `from` through `through`.
    @discardableResult
    private func feed(
        _ tracker: inout LongProcTracker, minutes: ClosedRange<Int>, rule: LongProcRule? = nil,
        ignored: Set<String> = [], items: (Int) -> [ProcSample]
    ) -> [LongProc] {
        var out: [LongProc] = []
        for m in minutes {
            out = tracker.observe(items(m), skipped: false, at: t0.addingTimeInterval(Double(m * 60)),
                                  rule: rule ?? self.rule, ignored: ignored)
        }
        return out
    }

    func testSteadyHighFlagsAfterSustain() {
        var t = LongProcTracker()
        XCTAssertTrue(feed(&t, minutes: 0...29) { _ in [self.proc(60)] }.isEmpty)
        let out = feed(&t, minutes: 30...30) { _ in [self.proc(60)] }
        XCTAssertEqual(out.map(\.pid), [812])
        XCTAssertEqual(out.first?.highSince, t0)
        XCTAssertEqual(out.first?.highFor(at: t0.addingTimeInterval(1800)), 1800)
    }

    func testTwentyPercentDipsStayFlagged() {
        var t = LongProcTracker()
        let out = feed(&t, minutes: 0...90) { m in [self.proc(m % 5 == 4 ? 10 : 60)] }
        XCTAssertEqual(out.count, 1)
        XCTAssertEqual(out.first?.highSince, t0, "a dip in the first window keeps the run start")
    }

    func testThirtyPercentDipsDoNotFlag() {
        var t = LongProcTracker()
        let out = feed(&t, minutes: 0...120) { m in [self.proc(m % 10 >= 7 ? 10 : 60)] }
        XCTAssertTrue(out.isEmpty)
    }

    func testBelowLimitNeverFlags() {
        var t = LongProcTracker()
        XCTAssertTrue(feed(&t, minutes: 0...120) { _ in [self.proc(40)] }.isEmpty)
        XCTAssertEqual(t.trackedCount, 0)
    }

    func testReusedPidWithNewStartIsNewRun() {
        var t = LongProcTracker()
        feed(&t, minutes: 0...20) { _ in [self.proc(60)] }
        let other = started.addingTimeInterval(500)
        let out = feed(&t, minutes: 21...40) { _ in [self.proc(60, start: other)] }
        XCTAssertTrue(out.isEmpty, "the new process has only 19 minutes")
        let later = feed(&t, minutes: 41...51) { _ in [self.proc(60, start: other)] }
        XCTAssertEqual(later.first?.highSince, t0.addingTimeInterval(21 * 60))
    }

    func testProcessThatLeavesClears() {
        var t = LongProcTracker()
        XCTAssertEqual(feed(&t, minutes: 0...30) { _ in [self.proc(60)] }.count, 1)
        XCTAssertTrue(feed(&t, minutes: 31...31) { _ in [] }.isEmpty, "a killed process clears at once")
        feed(&t, minutes: 32...70) { _ in [] }
        XCTAssertEqual(t.trackedCount, 0, "no high point in the window")
    }

    func testShortAbsenceKeepsTheRun() {
        var t = LongProcTracker()
        feed(&t, minutes: 0...30) { _ in [self.proc(60)] }
        feed(&t, minutes: 31...32) { _ in [] }
        let out = feed(&t, minutes: 33...33) { _ in [self.proc(60)] }
        XCTAssertEqual(out.first?.highSince, t0)
    }

    func testSkippedAndMissingProcsHoldTheLevel() {
        var t = LongProcTracker()
        feed(&t, minutes: 0...30) { _ in [self.proc(60)] }
        let held = t.observe(nil, skipped: false, at: t0.addingTimeInterval(31 * 60), rule: rule, ignored: [])
        XCTAssertEqual(held.count, 1)
        let skipped = t.observe([], skipped: true, at: t0.addingTimeInterval(32 * 60), rule: rule, ignored: [])
        XCTAssertEqual(skipped.count, 1)
    }

    func testIgnoredNamesNeverFlagInAnyCase() {
        var t = LongProcTracker()
        let out = feed(&t, minutes: 0...40, ignored: ["orbstack helper"]) { _ in [self.proc(90)] }
        XCTAssertTrue(out.isEmpty)
        XCTAssertEqual(t.trackedCount, 0)
    }

    func testIgnoreRemovesATrackedProcess() {
        var t = LongProcTracker()
        feed(&t, minutes: 0...30) { _ in [self.proc(60)] }
        let out = feed(&t, minutes: 31...31, ignored: ["orbstack helper"]) { _ in [self.proc(60)] }
        XCTAssertTrue(out.isEmpty)
        XCTAssertEqual(t.trackedCount, 0)
    }

    func testSteadyDayKeepsFewChangePoints() {
        var t = LongProcTracker()
        let day = LongProcRule(enabled: true, cpuPct: 50, sustainSeconds: 86_400, notify: .all, ignore: [])
        let out = feed(&t, minutes: 0...(24 * 60), rule: day) { m in
            [self.proc(m % 30 == 15 ? 10 : 60), self.proc(70, pid: 2, name: "node")]
        }
        XCTAssertEqual(out.count, 2)
        XCTAssertLessThan(t.changePointCount, 200)
        XCTAssertLessThan(feed(&t, minutes: 1441...1441, rule: rule) { _ in [self.proc(60)] }.count, 3)
        XCTAssertLessThan(t.changePointCount, 100, "a short rule trims the window")
    }

    func testDisabledRuleClears() {
        var t = LongProcTracker()
        feed(&t, minutes: 0...30) { _ in [self.proc(60)] }
        var off = rule
        off.enabled = false
        XCTAssertTrue(t.observe([proc(60)], skipped: false, at: t0.addingTimeInterval(1900), rule: off, ignored: []).isEmpty)
        XCTAssertEqual(t.trackedCount, 0)
    }

    func testSortedByCpu() {
        var t = LongProcTracker()
        let out = feed(&t, minutes: 0...30) { _ in [self.proc(60), self.proc(90, pid: 2, name: "node")] }
        XCTAssertEqual(out.map(\.name), ["node", "OrbStack Helper"])
    }

    func testKeyWithoutStartUsesName() {
        let a = ProcSample(pid: 5, name: "a", cpuPct: 1, rss: 1)
        let b = ProcSample(pid: 5, name: "b", cpuPct: 1, rss: 1)
        XCTAssertNotEqual(LongProcKey(a), LongProcKey(b))
        XCTAssertEqual(LongProcKey(proc(1)), LongProcKey(proc(99, name: "renamed")))
    }

    @MainActor
    func testHydrateReplayFlagsAtOnce() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let rows = (0...30).map { m in
            HistoryStore.ProcHistoryRow(t: t0.addingTimeInterval(Double(m * 60)), root: false, items: [proc(60)])
        }
        vm.hydrateProcs(from: rows)
        XCTAssertEqual(vm.longProcs.map(\.pid), [812])
        XCTAssertEqual(vm.alertLevel, .warn)
    }
}
