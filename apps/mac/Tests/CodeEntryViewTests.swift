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

    func testPasteRejectsWrongText() {
        XCTAssertNil(CodeEntryView.digits(from: "48291"))
        XCTAssertNil(CodeEntryView.digits(from: "4829135"))
        XCTAssertNil(CodeEntryView.digits(from: "48291a"))
        XCTAssertNil(CodeEntryView.digits(from: ""))
    }
}
