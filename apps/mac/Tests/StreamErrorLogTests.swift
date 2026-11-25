import XCTest
@testable import Towertail

final class StreamErrorLogTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)

    private func error(_ code: Int, _ info: String = "") -> NSError {
        NSError(domain: NSPOSIXErrorDomain, code: code, userInfo: ["task": info])
    }

    func testFirstErrorLogs() {
        var log = StreamErrorLog()
        XCTAssertTrue(log.shouldLog(error(100), now: t0))
    }

    func testSameErrorRepeatsAfterTenMinutes() {
        var log = StreamErrorLog()
        XCTAssertTrue(log.shouldLog(error(100, "a"), now: t0))
        XCTAssertFalse(log.shouldLog(error(100, "b"), now: t0.addingTimeInterval(5)))
        XCTAssertFalse(log.shouldLog(error(100, "c"), now: t0.addingTimeInterval(599)))
        XCTAssertTrue(log.shouldLog(error(100, "d"), now: t0.addingTimeInterval(600)))
    }

    func testNewErrorLogsAtOnce() {
        var log = StreamErrorLog()
        XCTAssertTrue(log.shouldLog(error(100), now: t0))
        XCTAssertTrue(log.shouldLog(error(61), now: t0.addingTimeInterval(1)))
        XCTAssertTrue(log.shouldLog(error(100), now: t0.addingTimeInterval(2)))
    }

    func testTextHasDomainAndCode() {
        let text = StreamErrorLog.text(error(100))
        XCTAssertTrue(text.hasPrefix("\(NSPOSIXErrorDomain) 100: "), text)
    }
}
