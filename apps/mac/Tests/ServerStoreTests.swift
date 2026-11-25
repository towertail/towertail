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

    func testCloudHostSkipsLocalRules() {
        let cloud = Node(displayName: "c", kind: .cloud)
        let ssh = Node(displayName: "s", kind: .ssh, sshUser: "u", sshHost: "s")
        let store = ServerStore(nodeLookup: { $0 == cloud.id ? cloud : ssh })
        let notifier = ThresholdNotifier(settings: ServerSettings())
        store.notifier = notifier
        for node in [cloud, ssh] {
            store.register(ServerViewModel(id: node.id, hostname: node.displayName, dnsName: node.displayName, osArch: "x", kind: node.kind))
            store.ingest(makeSample(cpuPct: 99, memFrac: 0.1, diskFrac: 0.1), for: node.id)
        }
        XCTAssertFalse(notifier.lastTint.keys.contains { $0.hostId == cloud.id })
        XCTAssertTrue(notifier.lastTint.keys.contains { $0.hostId == ssh.id })
    }
}
