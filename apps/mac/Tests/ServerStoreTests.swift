import XCTest
@testable import Towertail

@MainActor
final class ServerStoreTests: XCTestCase {
    func testStaleLimit() {
        XCTAssertEqual(ServerStore.staleLimit(pollSeconds: 2), 30)
        XCTAssertEqual(ServerStore.staleLimit(pollSeconds: 5), 30)
        XCTAssertEqual(ServerStore.staleLimit(pollSeconds: 10), 30)
        XCTAssertEqual(ServerStore.staleLimit(pollSeconds: 30), 90)
    }

    func testCheckStaleSkipsDisabledNode() {
        var node = Node(displayName: "h", kind: .ssh, sshUser: "u", sshHost: "h")
        node.enabled = false
        let store = ServerStore(nodeLookup: { _ in node })
        let vm = ServerViewModel(id: node.id, hostname: "h", dnsName: "h", osArch: "x", kind: .ssh)
        vm.everConnected = true
        store.register(vm)
        store.checkStale(now: Date().addingTimeInterval(3600), pollSeconds: { _ in 10 })
        XCTAssertNil(vm.noDataSince)
    }
}
