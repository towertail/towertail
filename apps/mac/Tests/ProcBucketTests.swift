import XCTest
@testable import Towertail

final class ProcBucketTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    func testTierByAge() {
        XCTAssertEqual(ProcBucket.of(now.addingTimeInterval(-59 * 60), now: now).tier, .raw)
        XCTAssertEqual(ProcBucket.of(now.addingTimeInterval(-61 * 60), now: now).tier, .minute)
        XCTAssertEqual(ProcBucket.of(now.addingTimeInterval(-7 * 86_400), now: now).tier, .minute)
        XCTAssertEqual(ProcBucket.of(now.addingTimeInterval(-7 * 86_400 - 1), now: now).tier, .hour)
    }

    func testStartRoundsDown() {
        let cal = Calendar(identifier: .gregorian)
        var c = DateComponents(timeZone: TimeZone(identifier: "UTC"), year: 2026, month: 4, day: 20, hour: 12, minute: 0, second: 37)
        let t = cal.date(from: c)!
        let at = t.addingTimeInterval(5)
        let raw = ProcBucket.of(t, now: at)
        c.second = 30
        XCTAssertEqual(raw.start, cal.date(from: c))
        let minute = ProcBucket.of(t, now: t.addingTimeInterval(2 * 3_600))
        c.second = 0
        XCTAssertEqual(minute.start, cal.date(from: c))
    }

    func testShiftedKeepsTier() {
        let b = ProcBucket.of(now.addingTimeInterval(-2 * 3_600), now: now)
        let n = b.shifted(-2)
        XCTAssertEqual(n.tier, b.tier)
        XCTAssertEqual(n.start, b.start.addingTimeInterval(-120))
        XCTAssertEqual(n.limit, b.limit)
    }
}
