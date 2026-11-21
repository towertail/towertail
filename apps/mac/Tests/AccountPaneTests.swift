import XCTest
@testable import Towertail

@MainActor
final class AccountPaneTests: XCTestCase {
    func testExitTextForEachPhase() {
        XCTAssertEqual(AccountSection.exitText(.idle, hosts: 3), "")
        XCTAssertEqual(AccountSection.exitText(.moving, hosts: 3), "The app moves 3 hosts back to SSH.")
        XCTAssertEqual(AccountSection.exitText(.moving, hosts: 1), "The app moves 1 host back to SSH.")
        XCTAssertEqual(AccountSection.exitText(.deleting, hosts: 3), "The app deletes the Cloud account.")
        XCTAssertEqual(AccountSection.exitText(.done, hosts: 3), "Done.")
        XCTAssertEqual(AccountSection.exitText(.failed("No."), hosts: 3), "No.")
    }

    func testSignOutMessage() {
        XCTAssertEqual(
            AccountSection.signOutMessage(hosts: 3, cloudOnly: 0),
            "3 hosts now report to Cloud. The app removes their samplers and deletes them from Cloud. This Mac then watches them over SSH again. Cloud alerts for these hosts stop."
        )
        XCTAssertEqual(
            AccountSection.signOutMessage(hosts: 0, cloudOnly: 2),
            "No host on this Mac reports to Cloud. 2 hosts exist only in Cloud. They stay in Cloud and leave this Mac."
        )
    }

    func testDeleteSheetNeedsMatchingEmailAndNoBlock() {
        let org = CloudOrgDeletion(id: UUID(), name: "Acme", role: "owner", fate: "delete", members: 1, nodes: 3, reason: nil)
        let ok = CloudAccountDeletion(email: "ana@acme.dev", orgs: [org], blocked: false)
        let blocked = CloudAccountDeletion(email: "ana@acme.dev", orgs: [org], blocked: true)
        XCTAssertFalse(DeleteCloudAccountSheet.canDelete(ok, typed: "ana@acme", busy: false))
        XCTAssertTrue(DeleteCloudAccountSheet.canDelete(ok, typed: " Ana@Acme.dev ", busy: false))
        XCTAssertFalse(DeleteCloudAccountSheet.canDelete(blocked, typed: "ana@acme.dev", busy: false))
        XCTAssertFalse(DeleteCloudAccountSheet.canDelete(ok, typed: "ana@acme.dev", busy: true))
        XCTAssertFalse(DeleteCloudAccountSheet.canDelete(nil, typed: "ana@acme.dev", busy: false))
    }
}
