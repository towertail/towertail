import XCTest
@testable import Towertail

@MainActor
final class CodeEntryViewTests: XCTestCase {
    func testPasteWithSpace() {
        XCTAssertEqual(CodeEntryView.digits(from: "482 913"), "482913")
    }

    func testPasteWithoutSpace() {
        XCTAssertEqual(CodeEntryView.digits(from: "482913"), "482913")
    }

    func testKeepsOnlyDigits() {
        XCTAssertEqual(CodeEntryView.digits(from: "48a2-91"), "48291")
        XCTAssertEqual(CodeEntryView.digits(from: ""), "")
    }

    func testStopsAtSixDigits() {
        XCTAssertEqual(CodeEntryView.digits(from: "4829135"), "482913")
    }
}
