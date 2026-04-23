import XCTest
@testable import Towertail

@MainActor
final class LocalBackendTests: XCTestCase {
    func testInstantiates() {
        let b = LocalBackend()
        // Observable surfaces wired to real concrete stores.
        XCTAssertNotNil(b.servers)
        XCTAssertNotNil(b.nodes)
        // Client + server settings are distinct objects, both loaded
        // from the same on-disk JSON.
        XCTAssertFalse(b.clientSettings === (b.serverSettings as AnyObject))
    }

    func testNodeCRUDRoundTrips() async throws {
        let b = LocalBackend()
        let initial = b.nodes.nodes.count
        let n = Node(displayName: "unit-test", kind: .ssh, sshUser: "u", sshHost: "h")

        try await b.addNode(n)
        XCTAssertEqual(b.nodes.nodes.count, initial + 1)
        XCTAssertNotNil(b.nodes.node(withId: n.id))

        var updated = n
        updated.displayName = "unit-test-renamed"
        try await b.updateNode(updated)
        XCTAssertEqual(b.nodes.node(withId: n.id)?.displayName, "unit-test-renamed")

        try await b.setNodeEnabled(id: n.id, enabled: false)
        XCTAssertEqual(b.nodes.node(withId: n.id)?.enabled, false)

        try await b.setNodeFavorite(id: n.id, favorite: true)
        XCTAssertEqual(b.nodes.node(withId: n.id)?.favorite, true)

        let until = Date().addingTimeInterval(60)
        try await b.setNodeSnooze(id: n.id, until: until)
        XCTAssertNotNil(b.nodes.node(withId: n.id)?.snoozedUntil)

        try await b.removeNode(id: n.id)
        XCTAssertNil(b.nodes.node(withId: n.id))
    }
}
