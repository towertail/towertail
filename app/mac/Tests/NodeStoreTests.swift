import XCTest
@testable import Towertail

@MainActor
final class NodeStoreTests: XCTestCase {
    private func tempURL() -> URL {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("towertail-tests-\(UUID().uuidString)", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("settings.json")
    }

    func testAddRemoveUpdate() {
        let url = tempURL()
        let store = NodeStore(nodes: [], url: url)
        let n = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db.internal")
        store.add(n)
        XCTAssertEqual(store.nodes.count, 1)

        var updated = n
        updated.displayName = "db-primary"
        store.update(updated)
        XCTAssertEqual(store.nodes.first?.displayName, "db-primary")

        store.remove(id: n.id)
        XCTAssertTrue(store.nodes.isEmpty)
    }

    func testPersistAndReload() {
        let url = tempURL()
        let store = NodeStore(nodes: [], url: url)
        let local = Node.localMac()
        let remote = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db.internal", tags: ["prod"], enabled: false)
        store.add(local)
        store.add(remote)

        let reloaded = NodeStore.loadFromDisk(url: url)
        XCTAssertEqual(reloaded.nodes.count, 2)
        XCTAssertEqual(reloaded.nodes[0], local)
        XCTAssertEqual(reloaded.nodes[1], remote)
        XCTAssertFalse(reloaded.nodes[1].enabled)
    }

    func testMalformedJsonFallsBackToDefault() throws {
        let url = tempURL()
        try "not json".write(to: url, atomically: true, encoding: .utf8)
        let reloaded = NodeStore.loadFromDisk(url: url)
        XCTAssertEqual(reloaded.nodes.count, 1)
        XCTAssertEqual(reloaded.nodes.first?.kind, .local)
    }

    func testSetEnabledRoundTrips() {
        let url = tempURL()
        let store = NodeStore(nodes: [], url: url)
        let remote = Node(displayName: "x", kind: .ssh, sshUser: "u", sshHost: "h")
        store.add(remote)
        store.setEnabled(id: remote.id, enabled: false)

        let reloaded = NodeStore.loadFromDisk(url: url)
        XCTAssertEqual(reloaded.nodes.first?.enabled, false)
    }
}
