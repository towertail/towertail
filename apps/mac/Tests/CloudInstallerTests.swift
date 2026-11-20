import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudInstallerTests: XCTestCase {
    private let token = "cloud-user-token"
    private var server: LocalTestServer!
    private var nodes: NodeStore!
    private var settings: ServerSettings!
    private var session: FakeSamplerSession!
    private var node: Node!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-installer-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("settings.json")
        node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        nodes = NodeStore(nodes: [node], url: url)
        settings = ServerSettings(url: url)
        session = FakeSamplerSession()
    }

    override func tearDown() async throws {
        server.stop()
        try await super.tearDown()
    }

    private func installer(firstSample: @escaping CloudInstaller.FirstSample) -> CloudInstaller {
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let session = self.session!
        return CloudInstaller(
            client: { client },
            nodes: nodes,
            settings: settings,
            planName: { "free" },
            firstSample: firstSample,
            connect: { _ in session },
            upload: { _, _ in "/home/u/.towertail/bin/towertail-sampler" },
            binary: { _ in URL(fileURLWithPath: "/tmp/towertail-sampler") },
            clock: ImmediateClock()
        )
    }

    private func serveEnroll() {
        let id = node.id
        server.on("POST", "/v1/sampler/enroll") { _ in
            let body = "{\"node_id\":\"\(id.uuidString)\",\"sampler_token\":\"tok\",\"endpoint\":\"https://api.example.com\"}"
            return .json(200, Data(body.utf8))
        }
    }

    func testRunMarksAllStepsDone() async throws {
        serveEnroll()
        let installer = installer { _, _ in "1.4.0+abc" }
        await installer.run(nodes: [node])

        let rows = try XCTUnwrap(installer.rows[node.id])
        for step in InstallStep.allCases {
            guard case .done = rows[step] else { return XCTFail("\(step) is \(String(describing: rows[step]))") }
        }
        let cloud = try XCTUnwrap(nodes.node(withId: node.id)?.cloud)
        XCTAssertEqual(cloud.reboot, "linger")
        XCTAssertEqual(cloud.samplerVersion, "1.4.0+abc")
        XCTAssertTrue(session.closed)
    }

    func testRunPlanFullKeepsSSH() async {
        server.on("POST", "/v1/sampler/enroll") { _ in
            .json(402, Data("{\"error\":{\"code\":\"plan_limit\",\"message\":\"the plan allows 3 hosts\"}}".utf8))
        }
        let installer = installer { _, _ in nil }
        await installer.run(nodes: [node])

        XCTAssertNil(nodes.node(withId: node.id)?.cloud)
        XCTAssertEqual(installer.results[node.id], "Free plan is full (3 of 3)")
        XCTAssertEqual(installer.notes[node.id], "Live view over SSH still works")
        XCTAssertEqual(installer.rows[node.id]?[.start], .waiting)
    }

    func testRunNoFirstSampleTimesOut() async {
        serveEnroll()
        let installer = installer { _, _ in nil }
        await installer.run(nodes: [node])

        XCTAssertEqual(installer.rows[node.id]?[.sample], .failed("No sample yet. Check the sampler log."))
        XCTAssertNotNil(nodes.node(withId: node.id)?.cloud)
    }

    func testRunWindowsHostIsNotSupported() async {
        session.fail("uname -sm", RemoteCommandFailed(exitCode: 1, stderr: "not recognized"))
        let installer = installer { _, _ in nil }
        await installer.run(nodes: [node])

        XCTAssertEqual(installer.rows[node.id]?[.copy], .failed("Not supported on this host"))
        XCTAssertNil(nodes.node(withId: node.id)?.cloud)
    }

    func testTurnOffClearsLink() async throws {
        var linked = node!
        linked.cloud = CloudLink(enrolledAt: Date())
        nodes.update(linked)
        let echo = try JSONEncoder().encode(linked.toRemote(global: .defaults))
        server.on("PUT", "/v1/nodes/") { _ in .json(200, echo) }

        try await installer { _, _ in nil }.turnOff(node: linked)

        let put = try XCTUnwrap(server.captured.first { $0.method == "PUT" })
        XCTAssertTrue(put.path.hasSuffix("/v1/nodes/\(linked.id.uuidString)"))
        XCTAssertTrue(String(decoding: put.body, as: UTF8.self).contains("\"enabled\":false"))
        let stored = try XCTUnwrap(nodes.node(withId: linked.id))
        XCTAssertNil(stored.cloud)
        XCTAssertTrue(stored.enabled)
    }
}
