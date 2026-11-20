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
}
