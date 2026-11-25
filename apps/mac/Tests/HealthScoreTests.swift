import XCTest
@testable import Towertail

@MainActor
final class HealthScoreTests: XCTestCase {
    private struct Row: Decodable {
        let value, warn, critical, score: Double
        let tint: String
    }

    private let t = MetricThresholds.defaults

    private func score(
        info: HealthInfo? = nil, cpu: Double? = nil, mem: Double? = nil, disk: Double? = nil,
        longProcs: [LongProc] = [], muted: MutedSignals = .empty
    ) -> HealthScore {
        HealthScore(
            info: info, disks: nil, cpu: cpu, mem: mem, disk: disk,
            longProcs: longProcs, thresholds: t, muted: muted
        )
    }

    private func longProc() -> LongProc {
        let p = ProcSample(pid: 7, name: "node", user: "u", cpuPct: 90, rss: 1, threads: 1, startTS: nil)
        return LongProc(key: LongProcKey(p), pid: 7, name: "node", cmd: nil, cpuPct: 90, highSince: Date(), startTS: nil)
    }

    func testBandsFromFixtureTable() throws {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "health-score-cases", withExtension: "json"))
        let rows = try JSONDecoder().decode([Row].self, from: Data(contentsOf: url))
        XCTAssertFalse(rows.isEmpty)
        for r in rows {
            let s = HealthScore.score(value: r.value, warn: r.warn, critical: r.critical)
            XCTAssertEqual(s, r.score, accuracy: 0.01, "\(r)")
            let tint = HealthScore.tint(score: s)
            XCTAssertEqual("\(tint)", r.tint, "\(r)")
        }
    }

    func testScoreIsWorstPart() {
        let s = score(cpu: 0.5, mem: 0.8, disk: 0.1)
        XCTAssertEqual(s.worst?.check, .mem)
        XCTAssertEqual(s.score, s.parts.map(\.score).min())
        XCTAssertEqual(s.parts.map(\.score), s.parts.map(\.score).sorted())
    }

    func testTintAgreesWithHealthStatus() {
        for procs in [1_000, 4_999, 5_000, 6_000, 19_999, 20_000, 25_000] {
            let info = HealthInfo(procs: procs, zombies: procs / 10)
            let status = HealthStatus(info: info, disks: nil, thresholds: t)
            let s = score(info: info)
            XCTAssertEqual(s.tint, status.tint, "procs \(procs)")
        }
    }

    func testMutedCheckIsSkipped() {
        let loud = score(cpu: 0.95)
        XCTAssertEqual(loud.tint, .critical)
        let quiet = score(cpu: 0.95, muted: MutedSignals([.cpu]))
        XCTAssertTrue(quiet.parts.isEmpty)
        XCTAssertEqual(quiet.score, 100)
    }

    func testLongProcScores70() {
        let s = score(longProcs: [longProc()])
        XCTAssertEqual(s.score, 70)
        XCTAssertEqual(s.tint, .warn)
    }

    func testNoPartsScores100() {
        let s = score()
        XCTAssertEqual(s.score, 100)
        XCTAssertEqual(s.tint, .nominal)
        XCTAssertNil(s.worst)
    }
}
