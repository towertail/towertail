import XCTest
@testable import Towertail

final class SSHBootstrapTests: XCTestCase {
    func testMapsUnameToTriple() throws {
        let cases: [(String, String)] = [
            ("Linux x86_64\n", "linux-amd64"),
            ("Linux aarch64", "linux-arm64"),
            ("Linux armv7l", "linux-armv7"),
            ("Linux armv7", "linux-armv7"),
            ("Darwin arm64", "darwin-arm64"),
            ("Darwin x86_64", "darwin-amd64"),
        ]
        for (uname, want) in cases {
            XCTAssertEqual(try SSHBootstrap.triple(fromUname: uname), want, uname)
        }
    }

    func testRejectsUnknownUname() {
        XCTAssertThrowsError(try SSHBootstrap.triple(fromUname: "FreeBSD amd64"))
        XCTAssertThrowsError(try SSHBootstrap.triple(fromUname: "Linux mips"))
        XCTAssertThrowsError(try SSHBootstrap.triple(fromUname: "garbage"))
    }
}
