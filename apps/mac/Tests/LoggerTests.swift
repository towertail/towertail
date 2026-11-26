import XCTest
@testable import Towertail

final class LoggerTests: XCTestCase {
    func testLevelOrder() {
        let order: [Logger.Level] = [.debug, .info, .warn, .error]
        XCTAssertEqual(Logger.Level.allCases.sorted(), order)
        XCTAssertEqual(order.map(\.rank), [0, 1, 2, 3])
    }
}
