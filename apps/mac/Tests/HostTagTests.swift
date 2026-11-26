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

    func testTagAndLabel() {
        let cloudOnly = Node(displayName: "web", kind: .cloud)
        let cases: [(name: String, node: Node, signedIn: Bool, connected: Bool, alerted: Bool, expected: HostTag?)] = [
            ("local", local, true, true, false, .local),
            ("local signed out", local, false, true, false, .local),
            ("cloud-linked ssh", cloud, true, true, false, .cloud),
            ("cloud kind", cloudOnly, true, true, false, .cloud),
            ("ssh only", ssh, true, true, false, .sshOnly),
            ("ssh signed out", ssh, false, true, false, nil),
            ("cloud alerted", cloud, true, true, true, .offlineAlerted),
            ("stream down", cloud, true, false, false, .cloudOffline),
            ("stream down alerted", cloud, true, false, true, .offlineAlerted),
        ]
        for c in cases {
            XCTAssertEqual(tag(c.node, signedIn: c.signedIn, connected: c.connected, alerted: c.alerted),
                           c.expected, c.name)
        }
        let labels: [(HostTag, String)] = [
            (.local, "LOCAL"), (.cloud, "CLOUD"), (.sshOnly, "SSH ONLY"), (.offlineAlerted, "OFFLINE · ALERTED"),
        ]
        for (t, label) in labels { XCTAssertEqual(t.label, label) }
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
