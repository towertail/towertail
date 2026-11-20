import XCTest
@testable import Towertail

@MainActor
final class HostTagTests: XCTestCase {
    private let local = Node.localMac()
    private let ssh = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
    private let cloud = Node(displayName: "web", kind: .ssh, sshUser: "ops", sshHost: "web",
                             cloud: CloudLink(enrolledAt: Date()))

    private func tag(_ node: Node, signedIn: Bool = true, connected: Bool = true, alerted: Bool = false) -> HostTag? {
        HostTag.tag(for: node, signedIn: signedIn, feedConnected: connected, alerted: alerted)
    }

    func testLocal() {
        XCTAssertEqual(tag(local), .local)
        XCTAssertEqual(tag(local, signedIn: false), .local)
        XCTAssertEqual(HostTag.local.label, "LOCAL")
    }

    func testCloud() {
        XCTAssertEqual(tag(cloud), .cloud)
        XCTAssertEqual(tag(Node(displayName: "web", kind: .cloud)), .cloud)
        XCTAssertEqual(HostTag.cloud.label, "CLOUD")
    }

    func testSSHOnly() {
        XCTAssertEqual(tag(ssh), .sshOnly)
        XCTAssertEqual(HostTag.sshOnly.label, "SSH ONLY")
    }

    func testOfflineAlerted() {
        XCTAssertEqual(tag(cloud, alerted: true), .offlineAlerted)
        XCTAssertEqual(HostTag.offlineAlerted.label, "OFFLINE · ALERTED")
    }

    func testSSHOnlyHiddenWhenSignedOut() {
        XCTAssertNil(tag(ssh, signedIn: false))
    }

    func testCloudOfflineWhenStreamIsDown() {
        XCTAssertEqual(tag(cloud, connected: false), .cloudOffline)
        XCTAssertEqual(tag(cloud, connected: false, alerted: true), .offlineAlerted)
    }

    func testTagForNodeWithoutCloudSession() {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("tt-tag-\(UUID().uuidString).json")
        let store = NodeStore(nodes: [local, ssh, cloud], url: url)
        XCTAssertEqual(HostTag.tag(forNode: local.id, in: store, cloud: nil), .local)
        XCTAssertNil(HostTag.tag(forNode: ssh.id, in: store, cloud: nil))
        XCTAssertEqual(HostTag.tag(forNode: cloud.id, in: store, cloud: nil), .cloudOffline)
        XCTAssertNil(HostTag.tag(forNode: UUID(), in: store, cloud: nil))
    }
}
