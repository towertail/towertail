import XCTest
@testable import Towertail

@MainActor
final class CloudMoveTests: XCTestCase {
    private var dir: URL!
    private var nodes: NodeStore!
    private var defaults: UserDefaults!

    override func setUp() async throws {
        try await super.setUp()
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-move-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        nodes = NodeStore(nodes: [Node.localMac()], url: dir.appendingPathComponent("nodes.json"))
        defaults = UserDefaults(suiteName: "tt-move-\(UUID().uuidString)")!
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: dir)
        try await super.tearDown()
    }

    @discardableResult
    private func addHosts(_ count: Int) -> [Node] {
        let made = (0..<count).map { Node(displayName: "h\($0)", kind: .ssh, sshUser: "ana", sshHost: "10.0.0.\($0)") }
        nodes.addMany(made)
        return made
    }

    private func installer(room: Int?) -> CloudInstaller {
        CloudInstaller(
            client: { nil },
            nodes: nodes,
            settings: ServerSettings(url: dir.appendingPathComponent("settings.json")),
            planName: { "free" },
            room: { room },
            firstSample: { _, _ in nil }
        )
    }

    private func move(room: Int?, signedIn: Bool = true, billing: CloudBilling? = nil) -> CloudMove {
        CloudMove(
            nodes: nodes,
            installer: installer(room: room),
            signedIn: { signedIn },
            billing: { billing },
            defaults: defaults
        )
    }

    func testOpenSnapshotsSSHHostsAndDefaultPicks() async {
        var hosts = addHosts(5)
        hosts[0].cloud = CloudLink(enrolledAt: Date())
        nodes.update(hosts[0])
        let move = move(room: 3)
        await move.open()

        XCTAssertTrue(move.isPresented)
        XCTAssertEqual(move.phase, .pick)
        XCTAssertEqual(move.hosts.map(\.id), hosts.dropFirst().map(\.id))
        XCTAssertEqual(move.picked.map(\.id), hosts[1...3].map(\.id))
        XCTAssertEqual(move.notPicked, 1)
        XCTAssertEqual(move.picks.counter, "3 of 3 Cloud hosts selected")
    }

    func testToggleStopsAtRoom() async {
        let hosts = addHosts(3)
        let move = move(room: 2)
        await move.open()
        move.toggle(hosts[2].id)
        XCTAssertFalse(move.picks.ids.contains(hosts[2].id))
        move.toggle(hosts[0].id)
        move.toggle(hosts[2].id)
        XCTAssertEqual(move.picks.ids, [hosts[1].id, hosts[2].id])
        move.clear()
        XCTAssertTrue(move.picked.isEmpty)
    }

    func testOfferOnceOpensOneTime() async {
        addHosts(2)
        let move = move(room: 3)
        await move.offerOnce()
        XCTAssertTrue(move.isPresented)
        XCTAssertTrue(defaults.bool(forKey: CloudMove.offeredKey))

        move.close()
        await move.offerOnce()
        XCTAssertFalse(move.isPresented)
    }

    func testOfferOnceSkipsWhenFullOrNoHosts() async {
        let empty = move(room: 3)
        await empty.offerOnce()
        XCTAssertFalse(empty.isPresented)

        addHosts(2)
        let full = move(room: 0)
        await full.offerOnce()
        XCTAssertFalse(full.isPresented)

        let out = move(room: 3, signedIn: false)
        await out.offerOnce()
        XCTAssertFalse(out.isPresented)
        XCTAssertFalse(defaults.bool(forKey: CloudMove.offeredKey))
    }

    func testStartRunsPickedHosts() async {
        let hosts = addHosts(4)
        let installer = installer(room: 2)
        let move = CloudMove(nodes: nodes, installer: installer, signedIn: { true }, billing: { nil }, defaults: defaults)
        await move.open()
        await move.start()

        XCTAssertEqual(move.phase, .install)
        XCTAssertNotNil(installer.rows[hosts[0].id])
        XCTAssertNotNil(installer.rows[hosts[1].id])
        XCTAssertNil(installer.rows[hosts[2].id])
        XCTAssertTrue(installer.stayed.isEmpty)
    }

    func testSummaryText() {
        addHosts(4)
        let billing = CloudBilling(plan: CloudPlan(id: "free", maxNodes: 3), nodes: 1, maxNodes: 3)
        XCTAssertEqual(move(room: 2, billing: billing).summary, "1 of 3 Cloud hosts used. 4 hosts on SSH.")
        XCTAssertNil(move(room: 2, signedIn: false, billing: billing).summary)
        XCTAssertFalse(move(room: 2, billing: billing).isFull)

        let full = CloudBilling(plan: CloudPlan(id: "free", maxNodes: 3), nodes: 3, maxNodes: 3)
        XCTAssertTrue(move(room: 0, billing: full).isFull)
    }

    func testNoteWhenFull() {
        XCTAssertEqual(
            CloudPicks.note(room: 0, planLabel: "Free plan", used: true),
            "The Free plan is full. Turn off 24/7 monitoring on another host first."
        )
        XCTAssertEqual(
            CloudPicks.note(room: 1, planLabel: "Free plan", used: true),
            "Cloud hosts get 24/7 alerts, long history and the web and phone apps. The Free plan has room for 1 more Cloud host. SSH hosts have no limit."
        )
    }
}
