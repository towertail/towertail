import XCTest
@testable import Towertail

@MainActor
final class CloudRulesMoveTests: XCTestCase {
    private let token = "cloud-user-token"
    private var server: LocalTestServer!
    private var client: RemoteClient!
    private var dir: URL!
    private var settings: ServerSettings!
    private var nodes: NodeStore!
    private var defaults: UserDefaults!
    private var host: Node!
    private var rules: CloudRules!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-rmove-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        settings = ServerSettings(url: dir.appendingPathComponent("settings.json"))
        host = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        nodes = NodeStore(nodes: [host], url: dir.appendingPathComponent("nodes.json"))
        defaults = UserDefaults(suiteName: "tt-rmove-\(UUID().uuidString)")!
        rules = CloudRules(client: client, device: "test-mac", cache: nil, gap: 0.05)
    }

    override func tearDown() async throws {
        server.stop()
        try? FileManager.default.removeItem(at: dir)
        try await super.tearDown()
    }

    private func serveRules() throws {
        let view = RemoteRulesView(
            org: RemoteOrgRules(thresholds: settings.thresholds, alerts: settings.alertRules,
                                muted: settings.mutedSignals, longProc: settings.longProc),
            hosts: [.init(nodeId: host.id, host: RemoteHostRules(), effective: RemoteEffectiveRules())])
        let data = try JSONEncoder().encode(view)
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
    }

    private func makeMove(admin: Bool = true, signedIn: Bool = true) -> CloudRulesMove {
        CloudRulesMove(
            settings: settings, nodes: nodes,
            rules: { self.rules }, client: { signedIn ? self.client : nil },
            isAdmin: { admin }, device: "test-mac", defaults: defaults)
    }

    /// Two local rules differ: the org CPU pair and the memory override of the host.
    private func makeDiffer() {
        settings.thresholds.cpuWarn = 0.65
        settings.thresholds.cpuCritical = 0.9
        var node = host!
        node.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        node.ignoredProcs = ["pg_dump"]
        nodes.update(node)
    }

    private func serveRulesBeforeDiffer() throws {
        try serveRules()
        makeDiffer()
    }

    func testOffersOnceForAdmin() async throws {
        try serveRulesBeforeDiffer()
        let move = makeMove()
        await move.offerOnce()
        XCTAssertTrue(move.isPresented)
        XCTAssertEqual(move.rows.count, 3)
        XCTAssertEqual(move.pickedCount, 3)
        XCTAssertTrue(defaults.bool(forKey: CloudRulesMove.offeredKey))
        move.close()
        await move.offerOnce()
        XCTAssertFalse(move.isPresented, "the offer comes one time")
    }

    func testNoOfferForMember() async throws {
        try serveRulesBeforeDiffer()
        let move = makeMove(admin: false)
        XCTAssertFalse(move.canMove)
        await move.offerOnce()
        XCTAssertFalse(move.isPresented)
        XCTAssertFalse(makeMove(signedIn: false).canMove)
    }

    func testNoOfferWhenNothingDiffers() async throws {
        try serveRules()
        let move = makeMove()
        await move.offerOnce()
        XCTAssertFalse(move.isPresented)
        XCTAssertFalse(defaults.bool(forKey: CloudRulesMove.offeredKey))
    }

    func testStartSendsPickedRowsAndRefreshes() async throws {
        try serveRulesBeforeDiffer()
        server.on("POST", "/v1/rules/import") { _ in
            .json(200, Data(#"{"backupId":"11111111-1111-1111-1111-111111111111","hosts":1,"org":true}"#.utf8))
        }
        let move = makeMove()
        await move.open()
        XCTAssertEqual(move.phase, .pick)
        let ignored = try XCTUnwrap(move.rows.first { $0.rule == "Ignored processes" })
        move.toggle(ignored.id)
        XCTAssertEqual(move.pickedCount, 2)
        let before = server.captured.filter { $0.path == "/v1/rules" }.count

        await move.start()

        let post = try XCTUnwrap(server.captured.first { $0.method == "POST" && $0.path == "/v1/rules/import" })
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: post.body) as? [String: Any])
        XCTAssertEqual(json["client"] as? String, "mac")
        XCTAssertEqual(json["device"] as? String, "test-mac")
        XCTAssertNotNil(json["org"] as? [String: Any])
        let hosts = try XCTUnwrap(json["hosts"] as? [[String: Any]])
        XCTAssertEqual(hosts.count, 1)
        let sent = try XCTUnwrap(hosts[0]["rules"] as? [String: Any])
        XCTAssertNotNil(sent["thresholds"])
        XCTAssertNil(sent["ignoredProcs"], "the unpicked row is not sent")
        XCTAssertGreaterThan(server.captured.filter { $0.path == "/v1/rules" }.count, before, "the rules refresh")
        XCTAssertEqual(move.toast, "Moved 2 rules to the cloud. The Mac now reads them from the cloud.")
        XCTAssertFalse(move.isPresented)
    }

    func testFailureKeepsSheetOpen() async throws {
        try serveRulesBeforeDiffer()
        server.on("POST", "/v1/rules/import") { _ in .json(403, Data("{}".utf8)) }
        let move = makeMove()
        await move.open()
        await move.start()
        XCTAssertEqual(move.phase, .failed("Only an org admin can change this."))
        XCTAssertTrue(move.isPresented)
        XCTAssertNil(move.toast)
        XCTAssertEqual(move.rows.count, 3, "the rows stay for a new try")
    }

    func testLoadFailureShowsError() async throws {
        server.on("GET", "/v1/rules") { _ in .json(500, Data("{}".utf8)) }
        let move = makeMove()
        await move.open()
        XCTAssertTrue(move.isPresented)
        guard case .failed = move.phase else { return XCTFail("expected a failure, got \(move.phase)") }
        XCTAssertTrue(move.rows.isEmpty)
    }

    func testRulesBackupsAndRestore() async throws {
        let id = "22222222-2222-2222-2222-222222222222"
        server.on("GET", "/v1/rules/backups") { _ in
            .json(200, Data(#"[{"id":"\#(id)","at":"2026-11-25T10:00:00.250Z","origin":{"client":"mac","device":"m1"},"org":{},"hosts":{"33333333-3333-3333-3333-333333333333":{"muted":[]}}}]"#.utf8))
        }
        server.on("POST", "/v1/rules/backups/\(id)/restore") { _ in
            .json(200, Data(#"{"backupId":"44444444-4444-4444-4444-444444444444","hosts":1,"org":true}"#.utf8))
        }
        let list = try await client.rulesBackups()
        XCTAssertEqual(list.count, 1)
        XCTAssertEqual(list[0].origin?.source, "m1")
        XCTAssertNotNil(list[0].at)
        XCTAssertEqual(list[0].hosts.count, 1)
        let result = try await client.restoreRules(backupID: list[0].id, device: "test-mac")
        XCTAssertEqual(result.hosts, 1)
        XCTAssertTrue(result.org)
    }
}
