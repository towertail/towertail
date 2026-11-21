import XCTest
@testable import Towertail

private final class ConnectLog: @unchecked Sendable {
    private let lock = NSLock()
    private var _ids: Set<UUID> = []
    var ids: Set<UUID> { lock.withLock { _ids } }
    func add(_ id: UUID) { lock.withLock { _ = _ids.insert(id) } }
}

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

    private func installer(
        room: @escaping @MainActor () async -> Int? = { nil },
        connect: CloudInstaller.Connect? = nil,
        firstSample: @escaping CloudInstaller.FirstSample
    ) -> CloudInstaller {
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let session = self.session!
        return CloudInstaller(
            client: { client },
            nodes: nodes,
            settings: settings,
            planName: { "free" },
            room: room,
            firstSample: firstSample,
            connect: connect ?? { _ in session },
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

    func testRunKeepsFoldersOffOnTheServer() async throws {
        serveEnroll()
        server.on("GET", "/v1/nodes/") { [node] _ in
            let json = "{\"id\":\"\(node!.id.uuidString)\",\"displayName\":\"web\",\"kind\":\"ssh\",\"tags\":[],\"enabled\":true,\"iconOnWarn\":true,\"iconOnCritical\":true,\"notifyOnWarn\":true,\"notifyOnCritical\":true,\"favorite\":false}"
            return .json(200, Data(json.utf8))
        }
        server.on("PUT", "/v1/nodes/") { req in .json(200, req.body) }
        var off = node!
        off.sendProcFolders = false
        await installer { _, _ in "1.5.0+abc" }.run(nodes: [off])

        let put = try XCTUnwrap(server.captured.first { $0.method == "PUT" })
        let sent = try XCTUnwrap(JSONSerialization.jsonObject(with: put.body) as? [String: Any])
        XCTAssertEqual(sent["hideProcFolders"] as? Bool, true)
        // The switch is set before the sampler starts.
        let paths = server.captured.map { "\($0.method) \($0.path)" }
        let enroll = try XCTUnwrap(paths.firstIndex { $0.hasSuffix("/v1/sampler/enroll") })
        let hide = try XCTUnwrap(paths.firstIndex { $0.hasPrefix("PUT") })
        XCTAssertLessThan(enroll, hide)
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
        server.on("DELETE", "/v1/nodes/") { _ in .empty(204) }

        try await installer { _, _ in nil }.turnOff(node: linked)

        let delete = try XCTUnwrap(server.captured.first { $0.method == "DELETE" })
        XCTAssertTrue(delete.path.hasSuffix("/v1/nodes/\(linked.id.uuidString)"))
        XCTAssertNil(server.captured.first { $0.method == "PUT" })
        let stored = try XCTUnwrap(nodes.node(withId: linked.id))
        XCTAssertNil(stored.cloud)
        XCTAssertTrue(stored.enabled)
    }

    // MARK: room

    private func hosts(_ count: Int) -> [Node] {
        let list = (0..<count).map { Node(displayName: "h\($0)", kind: .ssh, sshUser: "u", sshHost: "h\($0)") }
        nodes.addMany(list)
        return list
    }

    private func serveEnrollAll() {
        server.on("POST", "/v1/sampler/enroll") { req in
            let id = (try? JSONSerialization.jsonObject(with: req.body) as? [String: Any])?["node_id"] as? String ?? UUID().uuidString
            let body = "{\"node_id\":\"\(id)\",\"sampler_token\":\"tok\",\"endpoint\":\"https://api.example.com\"}"
            return .json(200, Data(body.utf8))
        }
    }

    func testRunStopsAtRoom() async {
        serveEnrollAll()
        let list = hosts(5)
        let connected = ConnectLog()
        let session = self.session!
        let installer = installer(room: { 3 }, connect: { node in
            connected.add(node.id)
            return session
        }) { _, _ in "1.4.0+abc" }
        await installer.run(nodes: list)

        XCTAssertEqual(connected.ids, Set(list.prefix(3).map(\.id)))
        XCTAssertEqual(installer.stayed, Set(list.suffix(2).map(\.id)))
        for node in list.prefix(3) {
            XCTAssertNotNil(nodes.node(withId: node.id)?.cloud, node.displayName)
        }
        for node in list.suffix(2) {
            XCTAssertNil(nodes.node(withId: node.id)?.cloud)
            XCTAssertEqual(installer.results[node.id], "Stays on this Mac over SSH")
            XCTAssertNil(installer.rows[node.id])
        }
    }

    func testRunNilRoomInstallsAll() async {
        serveEnrollAll()
        let list = hosts(4)
        let installer = installer { _, _ in "1.4.0+abc" }
        await installer.run(nodes: list)
        XCTAssertTrue(installer.stayed.isEmpty)
        for node in list { XCTAssertNotNil(nodes.node(withId: node.id)?.cloud) }
    }

    func testOverRoomText() {
        let installer = installer { _, _ in nil }
        XCTAssertNil(installer.overRoomText(selected: 3, room: 3))
        XCTAssertEqual(installer.overRoomText(selected: 5, room: 2), "The Free plan has room for 2 more hosts. Select 2 or fewer.")
        XCTAssertEqual(installer.overRoomText(selected: 1, room: 0), "The Free plan is full. Turn off 24/7 monitoring on another host first.")
    }

    // MARK: leave

    private func link(_ node: Node) {
        var linked = node
        linked.cloud = CloudLink(enrolledAt: Date())
        nodes.update(linked)
    }

    func testLeaveRemovesSamplerAndNode() async throws {
        link(node)
        server.on("DELETE", "/v1/nodes/") { _ in .empty(204) }
        let failures = await installer { _, _ in nil }.leave(nodes: [nodes.node(withId: node.id)!])

        XCTAssertTrue(failures.isEmpty)
        XCTAssertTrue(session.commands.contains { $0.contains("stop") })
        XCTAssertTrue(session.commands.contains { $0.contains("rm -f ~/.towertail/sampler.token") })
        let delete = try XCTUnwrap(server.captured.first { $0.method == "DELETE" })
        XCTAssertTrue(delete.path.hasSuffix("/v1/nodes/\(node.id.uuidString)"))
        XCTAssertNil(nodes.node(withId: node.id)?.cloud)
    }

    func testLeaveSkipsCloudOnly() async {
        let only = Node(displayName: "edge", kind: .cloud, cloud: CloudLink(enrolledAt: Date()))
        nodes.add(only)
        let failures = await installer { _, _ in nil }.leave(nodes: [only])
        XCTAssertTrue(failures.isEmpty)
        XCTAssertTrue(server.captured.isEmpty)
        XCTAssertNotNil(nodes.node(withId: only.id))
    }

    func testLeaveReportsFailure() async {
        link(node)
        server.on("DELETE", "/v1/nodes/") { _ in .empty(500) }
        let failures = await installer { _, _ in nil }.leave(nodes: [nodes.node(withId: node.id)!])
        XCTAssertEqual(failures.keys.sorted { $0.uuidString < $1.uuidString }, [node.id])
        XCTAssertNotNil(nodes.node(withId: node.id)?.cloud)
    }

    func testLeaveSignedOut() async {
        link(node)
        let signedOut = CloudInstaller(
            client: { nil }, nodes: nodes, settings: settings, firstSample: { _, _ in nil },
            connect: { [session] _ in session! }
        )
        let failures = await signedOut.leave(nodes: [nodes.node(withId: node.id)!])
        XCTAssertEqual(failures[node.id], LifecycleError.notSignedIn.localizedDescription)
    }
}
