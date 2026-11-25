import XCTest
@testable import Towertail

final class CloudMeTests: XCTestCase {
    private func decode(_ json: String) throws -> CloudMe {
        try JSONDecoder().decode(CloudMe.self, from: Data(json.utf8))
    }

    func testDecodesRole() throws {
        let admin = try decode(#"{"user":{"email":"a@b.c"},"org":{"name":"O","plan":"team"},"role":"admin"}"#)
        XCTAssertEqual(admin.role, "admin")
        XCTAssertTrue(admin.isAdmin)
        XCTAssertTrue(admin.canMute)
    }

    func testMissingRoleIsNotAdmin() throws {
        let me = try decode(#"{"user":{"email":"a@b.c"},"org":{"name":"O","plan":"team"}}"#)
        XCTAssertNil(me.role)
        XCTAssertFalse(me.isAdmin)
        XCTAssertFalse(me.canMute)
    }

    func testRolesMapToRights() throws {
        func me(_ role: String) throws -> CloudMe {
            try decode(#"{"user":{"email":"a@b.c"},"org":{"name":"O","plan":"team"},"role":"\#(role)"}"#)
        }
        XCTAssertTrue(try me("owner").isAdmin)
        XCTAssertFalse(try me("member").isAdmin)
        XCTAssertTrue(try me("member").canMute)
        XCTAssertFalse(try me("viewer").canMute)
    }
}
