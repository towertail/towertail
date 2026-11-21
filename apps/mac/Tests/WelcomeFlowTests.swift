import XCTest
@testable import Towertail

@MainActor
final class WelcomeFlowTests: XCTestCase {
    private var dir: URL!
    private var settings: ClientSettings!
    private var nodes: NodeStore!

    private struct FakeDiscovery: ServerDiscovery {
        var found: [ServerCandidate] = []
        func tailscale() async throws -> [ServerCandidate] { found }
        func sshConfig() throws -> [ServerCandidate] { [] }
    }

    override func setUp() async throws {
        try await super.setUp()
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-welcome-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("settings.json")
        settings = ClientSettings(url: url)
        nodes = NodeStore(nodes: [Node.localMac()], url: url)
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: dir)
        try await super.tearDown()
    }

    private func flow(
        discovery: ServerDiscovery = FakeDiscovery(),
        close: @escaping () -> Void = {}
    ) -> WelcomeFlow {
        let flow = WelcomeFlow(
            nodes: nodes,
            settings: settings,
            cloud: nil,
            installer: nil,
            discovery: discovery,
            probe: { _ in (.ok(ms: 5), "Linux") },
            verify: { _ in }
        )
        flow.close = close
        return flow
    }

    private func candidate(_ name: String, host: String, user: String) -> ServerCandidate {
        ServerCandidate(id: "ssh:\(name)", name: name, address: host, user: user, source: .sshConfig)
    }

    func testShouldShowOnFirstLaunch() {
        XCTAssertTrue(WelcomeState.shouldShow(nodes: nodes.nodes, settings: settings))
        let ssh = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        XCTAssertFalse(WelcomeState.shouldShow(nodes: nodes.nodes + [ssh], settings: settings))
        settings.welcomeDone = true
        XCTAssertFalse(WelcomeState.shouldShow(nodes: nodes.nodes, settings: settings))
    }

    func testSkipSetsDone() throws {
        var closed = false
        flow(close: { closed = true }).skip()
        XCTAssertTrue(settings.welcomeDone)
        XCTAssertTrue(closed)
        let reloaded = ClientSettings(url: dir.appendingPathComponent("settings.json"))
        XCTAssertTrue(reloaded.welcomeDone)
    }

    func testNotNowEndsWithoutCloud() {
        var closed = false
        let flow = flow(close: { closed = true })
        flow.next()
        flow.next()
        XCTAssertEqual(flow.step, .cloud)
        flow.finish()
        XCTAssertTrue(closed)
        XCTAssertTrue(settings.welcomeDone)
        XCTAssertTrue(nodes.nodes.allSatisfy { $0.cloud == nil })
    }

    func testFlowOrder() {
        let flow = flow()
        var seen: [WelcomeFlow.Step] = [flow.step]
        for _ in 0..<4 {
            flow.next()
            seen.append(flow.step)
        }
        XCTAssertEqual(seen, [.welcome, .servers, .cloud, .signIn, .install])
        flow.back()
        XCTAssertEqual(flow.step, .install, "back is off at the install step")
    }

    func testAddSelectedMakesNodes() async throws {
        let a = candidate("web", host: "10.0.0.1", user: "ana")
        let b = candidate("db", host: "db.local", user: "bob")
        let c = candidate("skip", host: "10.0.0.3", user: "cy")
        let flow = flow(discovery: FakeDiscovery(found: [a, b, c]))
        await flow.loadSources()
        XCTAssertEqual(flow.candidates.count, 3)
        XCTAssertTrue(flow.candidates.allSatisfy { $0.test == .ok(ms: 5) })
        flow.selected = [a.id, b.id]
        await flow.addSelected()

        let ssh = nodes.nodes.filter { $0.kind == .ssh }
        XCTAssertEqual(ssh.map(\.sshHost), ["10.0.0.1", "db.local"])
        XCTAssertEqual(ssh.map(\.sshUser), ["ana", "bob"])
        XCTAssertEqual(flow.added.count, 2)
        XCTAssertEqual(flow.candidates.map(\.id), [c.id])
    }

    func testVerifyFailureKeepsNode() async {
        let a = candidate("web", host: "10.0.0.1", user: "ana")
        let flow = WelcomeFlow(
            nodes: nodes, settings: settings, cloud: nil, installer: nil,
            discovery: FakeDiscovery(found: [a]),
            probe: { _ in (.refused, nil) },
            verify: { _ in throw SamplerInvokeError.misconfigured("no key") }
        )
        await flow.loadSources()
        XCTAssertEqual(flow.keyWorks, 0)
        flow.selected = [a.id]
        await flow.addSelected()
        XCTAssertEqual(nodes.nodes.filter { $0.kind == .ssh }.count, 1)
        XCTAssertEqual(flow.addFailures.count, 1)
    }

    func testToggleAllFromSource() async {
        let a = candidate("web", host: "10.0.0.1", user: "ana")
        let b = candidate("db", host: "10.0.0.2", user: "bob")
        let flow = flow(discovery: FakeDiscovery(found: [a, b]))
        await flow.loadSources()
        flow.toggleAll(from: .sshConfig)
        XCTAssertEqual(flow.selected.count, 2)
        flow.toggleAll(from: .sshConfig)
        XCTAssertTrue(flow.selected.isEmpty)
    }

    // MARK: plan room

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

    /// A flow with `count` added SSH hosts.
    private func flow(hosts count: Int, installer: CloudInstaller, cloud: CloudSession? = nil, openURL: @escaping (URL) -> Void = { _ in }) async -> WelcomeFlow {
        let found = (0..<count).map { candidate("h\($0)", host: "10.0.0.\($0)", user: "ana") }
        let flow = WelcomeFlow(
            nodes: nodes, settings: settings, cloud: cloud, installer: installer,
            discovery: FakeDiscovery(found: found),
            probe: { _ in (.ok(ms: 5), "Linux") },
            verify: { _ in },
            openURL: openURL
        )
        await flow.loadSources()
        flow.selected = Set(found.map(\.id))
        await flow.addSelected()
        return flow
    }

    func testPickDefaultsToFirstRoomHosts() async {
        let flow = await flow(hosts: 5, installer: installer(room: 3))
        await flow.loadRoom()
        XCTAssertTrue(flow.needsPick)
        XCTAssertEqual(flow.cloudPicks, Set(flow.cloudCandidates.prefix(3).map(\.id)))
    }

    func testTogglePickStopsWhenFull() async {
        let flow = await flow(hosts: 5, installer: installer(room: 3))
        await flow.loadRoom()
        let last = flow.cloudCandidates[4].id
        flow.togglePick(last)
        XCTAssertFalse(flow.cloudPicks.contains(last))
        let first = flow.cloudCandidates[0].id
        flow.togglePick(first)
        XCTAssertEqual(flow.cloudPicks.count, 2)
        flow.togglePick(last)
        XCTAssertTrue(flow.cloudPicks.contains(last))
        flow.clearPicks()
        XCTAssertTrue(flow.cloudPicks.isEmpty)
    }

    func testNoPickerWhenRoomNil() async {
        let flow = await flow(hosts: 5, installer: installer(room: nil))
        await flow.loadRoom()
        XCTAssertFalse(flow.needsPick)
    }

    func testNoPickerWhenHostsFit() async {
        let flow = await flow(hosts: 3, installer: installer(room: 3))
        await flow.loadRoom()
        XCTAssertFalse(flow.needsPick)
    }

    func testRunUsesPickedHosts() async {
        let installer = installer(room: 2)
        let flow = await flow(hosts: 4, installer: installer)
        await flow.loadRoom()
        let hosts = flow.cloudCandidates
        flow.clearPicks()
        flow.togglePick(hosts[3].id)
        flow.togglePick(hosts[1].id)
        await flow.install()

        XCTAssertEqual(installer.stayed, [hosts[0].id, hosts[2].id])
        XCTAssertNotNil(installer.rows[hosts[3].id])
        XCTAssertNotNil(installer.rows[hosts[1].id])
        XCTAssertNil(installer.rows[hosts[0].id])
    }

    func testPlanNoteCountsStayed() async {
        let installer = installer(room: 3)
        let flow = await flow(hosts: 10, installer: installer)
        XCTAssertNil(flow.planNote)
        await flow.install()
        XCTAssertEqual(flow.planNote, "Free plan: 3 hosts go to Cloud. The other 7 hosts stay on this Mac over SSH.")
        XCTAssertTrue(flow.hasPlanFull)
    }

    func testNoUpgradeWhenPaymentsOff() async throws {
        let token = "user-token-1"
        let server = try LocalTestServer(token: token)
        try server.start()
        defer { server.stop() }
        server.on("GET", "/v1/me") { _ in
            .json(200, Data("""
            {"user":{"id":"u1","email":"ana@acme.dev"},"org":{"id":"o1","name":"Acme","plan":"free"},"logins":[]}
            """.utf8))
        }
        server.on("GET", "/v1/billing") { _ in
            .json(200, Data("""
            {"plan":{"id":"free","maxNodes":3},"nodes":0,"maxNodes":3,"payments":false}
            """.utf8))
        }
        server.on("GET", "/v1/nodes") { _ in .json(200, Data("[]".utf8)) }
        server.on("GET", "/v1/alerts") { _ in .json(200, Data("{\"alerts\":[]}".utf8)) }
        let keychain = FakeTokenKeychain()
        try keychain.set(token, account: "127.0.0.1:\(server.port)")
        let account = CloudAccount(
            endpoint: server.baseURL, keychain: keychain, client: CloudAuthClient(endpoint: server.baseURL),
            retryDelay: .seconds(3600), defaults: UserDefaults(suiteName: "tt-welcome-\(UUID().uuidString)")!
        )
        let session = CloudSession(account: account, servers: ServerStore(), nodes: nodes)
        await account.restore()
        defer { account.signOut() }

        var opened: [URL] = []
        let installer = installer(room: 3)
        let flow = await flow(hosts: 5, installer: installer, cloud: session, openURL: { opened.append($0) })
        await flow.install()
        XCTAssertTrue(flow.hasPlanFull)
        XCTAssertFalse(flow.canBuy)
        flow.upgrade()
        XCTAssertTrue(opened.isEmpty)
    }
}
