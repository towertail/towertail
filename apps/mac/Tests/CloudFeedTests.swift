import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudFeedTests: XCTestCase {
    private let token = "cloud-user-token"
    private var server: LocalTestServer!
    private var servers: ServerStore!
    private var nodes: NodeStore!
    private var feed: CloudFeed!
    private var released: ReleasedHosts!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        servers = ServerStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("tt-feed-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("settings.json")
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        nodes = NodeStore(nodes: [], url: url)
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        released = ReleasedHosts(defaults: UserDefaults(suiteName: "tt-feed-\(UUID().uuidString)")!)
        feed = CloudFeed(client: client, servers: servers, nodes: nodes, released: released)
    }

    override func tearDown() async throws {
        feed.stop()
        server.stop()
        try await super.tearDown()
    }

    private func serveNodes(_ list: [Node]) {
        let payload = (try? JSONEncoder().encode(list.map { $0.toRemote(global: .defaults) })) ?? Data()
        server.on("GET", "/v1/nodes") { _ in .json(200, payload) }
        server.on("GET", "/v1/alerts") { _ in .json(200, Data("{\"alerts\":[]}".utf8)) }
    }

    private func addHost(_ node: Node) {
        nodes.add(node)
        servers.register(ServerViewModel(id: node.id, hostname: node.displayName, dnsName: "x", osArch: "linux/arm64"))
    }

    private func sampleFrame(for id: UUID) throws -> String {
        let envelope: [String: Any] = [
            "type": "sample",
            "nodeId": id.uuidString,
            "sample": [
                "v": 1,
                "ts": "2026-04-21T12:00:00.000Z",
                "host": ["name": "h", "os": "linux", "arch": "arm64", "kernel": "6.6", "uptime_s": 1, "sampler": "0.1.0"],
                "cpu": ["pct": 10.0, "load_1": 0, "load_5": 0, "load_15": 0, "cores": 4],
                "mem": ["used": 1, "total": 2],
                "swap": ["used": 0, "total": 0],
                "errors": [],
            ] as [String: Any],
        ]
        return String(decoding: try JSONSerialization.data(withJSONObject: envelope), as: UTF8.self)
    }

    private func eventFrame(for id: UUID, tint: String) throws -> String {
        let envelope: [String: Any] = [
            "type": "event",
            "event": [
                "id": UUID().uuidString, "ts": "2026-04-21T12:00:00Z", "kind": "reachability_changed",
                "nodeId": id.uuidString, "metric": "reachability", "tint": tint,
            ],
        ]
        return String(decoding: try JSONSerialization.data(withJSONObject: envelope), as: UTF8.self)
    }

    private func waitFor(_ check: @escaping @MainActor () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            if check() { return }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        XCTFail("condition not met within 5s")
    }

    private func connect() async throws {
        feed.start()
        try await waitFor { self.server.hasWebSocketClient }
    }

    func testSampleForCloudNodeIsIngested() async throws {
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        try await connect()
        server.sendToAllWebSockets(text: try sampleFrame(for: node.id))
        try await waitFor { self.servers.serverVMs.first?.lastSeen != nil }
        XCTAssertEqual(servers.serverVMs.first?.samplerVersion, "0.1.0")
    }

    private func summaryFrame(for id: UUID) -> String {
        """
        {"type":"summary","nodeId":"\(id.uuidString)","summary":{"ts":"2026-04-21T12:00:00.000Z","cpu":10,"mem":0.5,"disk":0.2,"rx":0,"tx":0,"load":0,"host":{"os":"linux","arch":"arm64","cores":4,"mem_total":1,"uptime_s":1,"sampler":"0.2.0"}}}
        """
    }

    func testSummaryForCloudNodeIsIngestedAndSSHNodeIsIgnored() async throws {
        let cloud = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        let plain = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "d")
        serveNodes([cloud])
        addHost(cloud)
        addHost(plain)
        try await connect()
        server.sendToAllWebSockets(text: summaryFrame(for: plain.id))
        server.sendToAllWebSockets(text: summaryFrame(for: cloud.id))
        try await waitFor { self.servers.serverVMs.first(where: { $0.id == cloud.id })?.lastSeen != nil }
        XCTAssertEqual(servers.serverVMs.first(where: { $0.id == cloud.id })?.samplerVersion, "0.2.0")
        XCTAssertNil(servers.serverVMs.first(where: { $0.id == plain.id })?.lastSeen)
    }

    func testReachabilityEventMarksCloudHostOfflineAndBack() async throws {
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        try await connect()
        server.sendToAllWebSockets(text: summaryFrame(for: node.id))
        try await waitFor { self.servers.serverVMs.first?.lastSeen != nil }
        server.sendToAllWebSockets(text: try eventFrame(for: node.id, tint: "critical"))
        try await waitFor { self.servers.serverVMs.first?.state.isOffline == true }
        server.sendToAllWebSockets(text: try eventFrame(for: node.id, tint: "ok"))
        try await waitFor { self.servers.serverVMs.first?.state.isOffline == false }
    }

    func testFeedConnectsInLeaseModeAndSendsTheView() async throws {
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        feed.stop()
        var target: CloudFeed?
        let lease = ViewLeaseController(send: { target?.send($0) })
        let leased = CloudFeed(client: RemoteClient(config: .init(endpoint: server.baseURL, token: token)),
                               servers: servers, nodes: nodes, viewLease: lease)
        target = leased
        defer { leased.stop() }
        leased.start()
        try await waitFor { self.server.hasWebSocketClient }
        XCTAssertEqual(server.lastWebSocketPath?.hasSuffix("?lease=1"), true)

        server.sendToAllWebSockets(text: #"{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}"#)
        try await waitFor { self.servers.serverVMs.first?.liveSource == .cloud }

        lease.setPopover(visible: true)
        lease.setCard(node.id, visible: true)
        try await waitFor { self.server.receivedWebSocketTexts.contains { $0.contains(node.id.uuidString) } }
        let text = try XCTUnwrap(server.receivedWebSocketTexts.first { $0.contains("\"view\"") })
        XCTAssertTrue(text.contains("\"hosts\""))

        leased.stop()
        XCTAssertEqual(servers.serverVMs.first?.liveSource, .poll)
    }

    func testSampleForSSHNodeIsIgnored() async throws {
        let cloud = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        let plain = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "d")
        serveNodes([cloud])
        addHost(cloud)
        addHost(plain)
        try await connect()
        server.sendToAllWebSockets(text: try sampleFrame(for: plain.id))
        server.sendToAllWebSockets(text: try sampleFrame(for: cloud.id))
        try await waitFor { self.servers.serverVMs.first(where: { $0.id == cloud.id })?.lastSeen != nil }
        XCTAssertNil(servers.serverVMs.first(where: { $0.id == plain.id })?.lastSeen)
    }

    func testUnknownCloudNodeIsAdded() async throws {
        let unknown = Node(displayName: "from-web", kind: .ssh)
        serveNodes([unknown])
        await feed.syncNodes()
        let added = try XCTUnwrap(nodes.node(withId: unknown.id))
        XCTAssertEqual(added.kind, .cloud)
        XCTAssertEqual(added.displayName, "from-web")
        XCTAssertTrue(added.isCloud)
    }

    func testReleasedNodeIsRelinked() async throws {
        let local = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        nodes.add(local)
        released.add([local.id])
        serveNodes([local])
        await feed.syncNodes()
        let stored = try XCTUnwrap(nodes.node(withId: local.id))
        XCTAssertNotNil(stored.cloud)
        XCTAssertEqual(stored.kind, .ssh)
    }

    func testFailedInstallNodeIsNotRelinked() async throws {
        let local = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        nodes.add(local)
        serveNodes([local])
        await feed.syncNodes()
        XCTAssertNil(nodes.node(withId: local.id)?.cloud)
    }

    func testDisabledSSHNodeIsNotRelinked() async throws {
        var local = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        nodes.add(local)
        released.add([local.id])
        local.enabled = false
        serveNodes([local])
        await feed.syncNodes()
        XCTAssertNil(nodes.node(withId: local.id)?.cloud)
    }

    func testReleasedSetShrinks() async throws {
        let back = Node(displayName: "back", kind: .ssh, sshUser: "u", sshHost: "b")
        let gone = Node(displayName: "gone", kind: .ssh, sshUser: "u", sshHost: "g")
        nodes.add(back)
        nodes.add(gone)
        released.add([back.id, gone.id])
        serveNodes([back])
        await feed.syncNodes()
        XCTAssertTrue(released.ids.isEmpty)
    }

    func testFailedListKeepsReleasedSet() async {
        let local = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
        nodes.add(local)
        released.add([local.id])
        server.on("GET", "/v1/nodes") { _ in .empty(500) }
        await feed.syncNodes()
        XCTAssertEqual(released.ids, [local.id])
    }

    func testCloudNodeStillAdded() async throws {
        let unknown = Node(displayName: "edge", kind: .cloud)
        serveNodes([unknown])
        await feed.syncNodes()
        XCTAssertEqual(nodes.node(withId: unknown.id)?.kind, .cloud)
    }

    func testRemovedCloudNodeClearsLink() async throws {
        let linked = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        let cloudOnly = Node(displayName: "gone", kind: .cloud, cloud: CloudLink(enrolledAt: Date()))
        nodes.add(linked)
        nodes.add(cloudOnly)
        serveNodes([])
        await feed.syncNodes()
        XCTAssertNil(nodes.node(withId: linked.id)?.cloud)
        XCTAssertEqual(nodes.node(withId: linked.id)?.kind, .ssh)
        XCTAssertNil(nodes.node(withId: cloudOnly.id))
    }

    func testFailedListChangesNothing() async {
        let linked = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        nodes.add(linked)
        server.on("GET", "/v1/nodes") { _ in .empty(500) }
        await feed.syncNodes()
        XCTAssertNotNil(nodes.node(withId: linked.id)?.cloud)
    }

    func testReachabilityEventSetsAlerted() async throws {
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        try await connect()
        server.sendToAllWebSockets(text: try eventFrame(for: node.id, tint: "critical"))
        try await waitFor { self.feed.alerted.contains(node.id) }
        server.sendToAllWebSockets(text: try eventFrame(for: node.id, tint: "ok"))
        try await waitFor { !self.feed.alerted.contains(node.id) }
    }

    func testOpenAlertsSeedAlerted() async throws {
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        let body = """
        {"alerts":[{"id":"\(UUID().uuidString)","node_id":"\(node.id.uuidString)","kind":"reachability_changed","metric":"reachability","tint":"critical","ts":"2026-04-21T12:00:00Z","value":0},
        {"id":"\(UUID().uuidString)","node_id":"\(UUID().uuidString)","kind":"threshold_crossed","metric":"cpu","tint":"warn","ts":"2026-04-21T12:00:00Z","value":90}]}
        """
        server.on("GET", "/v1/alerts") { _ in .json(200, Data(body.utf8)) }
        addHost(node)
        try await connect()
        server.sendToAllWebSockets(text: try sampleFrame(for: node.id))
        try await waitFor { self.feed.alerted == [node.id] }
    }

    func testConnectedFollowsTheStream() async throws {
        serveNodes([])
        try await connect()
        // The test server does not answer a ping, so a frame ends the wait.
        server.sendToAllWebSockets(text: "{\"type\":\"hello\"}")
        try await waitFor { self.feed.connected }
        server.closeAllWebSockets()
        try await waitFor { !self.feed.connected }
    }

    @MainActor
    private final class FakePoster: CloudAlertPoster {
        var posted: [(StreamEvent, String)] = []
        var withdrawn: [UUID] = []
        func postCloud(event: StreamEvent, hostName: String) { posted.append((event, hostName)) }
        func withdrawCloud(alertId: UUID) { withdrawn.append(alertId) }
    }

    private func makeFeed(poster: FakePoster) -> CloudFeed {
        feed.stop()
        let f = CloudFeed(client: RemoteClient(config: .init(endpoint: server.baseURL, token: token)),
                          servers: servers, nodes: nodes, released: released, notifier: poster)
        feed = f
        return f
    }

    private func alertEventFrame(node: UUID, alert: UUID, notify: Bool) -> String {
        """
        {"type":"event","event":{"id":"\(UUID().uuidString)","alertId":"\(alert.uuidString)","ts":"2026-11-25T10:00:00Z","kind":"threshold_crossed","nodeId":"\(node.uuidString)","metric":"cpu","tint":"critical","notify":\(notify),"title":"web: CPU critical","text":"CPU at 97%."}}
        """
    }

    func testEventWithNotifyPostsCloudNotification() async throws {
        let poster = FakePoster()
        _ = makeFeed(poster: poster)
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        let plain = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "d")
        serveNodes([node])
        addHost(node)
        addHost(plain)
        try await connect()
        let alert = UUID()
        server.sendToAllWebSockets(text: alertEventFrame(node: node.id, alert: alert, notify: false))
        server.sendToAllWebSockets(text: alertEventFrame(node: plain.id, alert: alert, notify: true))
        server.sendToAllWebSockets(text: alertEventFrame(node: node.id, alert: alert, notify: true))
        try await waitFor { !poster.posted.isEmpty }
        XCTAssertEqual(poster.posted.count, 1)
        XCTAssertEqual(poster.posted.first?.0.alertId, alert)
        XCTAssertEqual(poster.posted.first?.1, "web")
    }

    func testAlertUpdatedWithdraws() async throws {
        let poster = FakePoster()
        _ = makeFeed(poster: poster)
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        try await connect()
        let alert = UUID()
        func frame(_ extra: String) -> String {
            """
            {"type":"alert_updated","nodeId":"\(node.id.uuidString)","alert":{"id":"\(alert.uuidString)","node_id":"\(node.id.uuidString)","kind":"threshold_crossed","metric":"cpu","tint":"critical","ts":"2026-11-25T10:00:00Z","value":0.97\(extra)}}
            """
        }
        server.sendToAllWebSockets(text: frame(""))
        server.sendToAllWebSockets(text: frame(#","acked_by":"\#(UUID().uuidString)""#))
        try await waitFor { !poster.withdrawn.isEmpty }
        XCTAssertEqual(poster.withdrawn, [alert])
    }

    // MARK: - Cloud rules

    private func attachRules(gap: TimeInterval = 0.05) -> CloudRules {
        let cache = CloudRulesCache(url: FileManager.default.temporaryDirectory
            .appendingPathComponent("tt-feed-rules-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("cloud-rules.json"))
        let rules = CloudRules(client: RemoteClient(config: .init(endpoint: server.baseURL, token: token)),
                               device: "test-mac", cache: cache, gap: gap)
        feed.rules = rules
        let empty = Data(#"{"org":{},"hosts":[]}"#.utf8)
        server.on("GET", "/v1/rules") { _ in .json(200, empty) }
        return rules
    }

    private func rulesFetches() -> Int {
        server.captured.filter { $0.method == "GET" && $0.path == "/v1/rules" }.count
    }

    func testSettingsUpdatedRefreshesRules() async throws {
        let rules = attachRules()
        defer { rules.stop() }
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        try await connect()
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        let settings = try enc.encode(RemoteServerSettings(
            thresholds: .defaults, localPollingIntervalSeconds: 2, sshPollingIntervalSeconds: 10,
            notificationsEnabled: true, notifyWarn: true, notifyCritical: true, notifyDebounceSeconds: 90,
            autoUpdateSamplersEnabled: true, postWakeGraceSeconds: 15))
        let frame = #"{"type":"settings_updated","settings":"# + String(decoding: settings, as: UTF8.self) + "}"
        let before = rulesFetches()
        server.sendToAllWebSockets(text: frame)
        try await waitFor { self.rulesFetches() > before }
    }

    func testNodeUpdatedRefreshesRulesOnce() async throws {
        let rules = attachRules(gap: 0.6)
        defer { rules.stop() }
        let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        serveNodes([node])
        addHost(node)
        try await connect()
        await rules.refresh()
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        let remote = try enc.encode(node.toRemote(global: .defaults))
        let frame = #"{"type":"node_updated","node":"# + String(decoding: remote, as: UTF8.self) + "}"
        let before = rulesFetches()
        for _ in 0..<4 { server.sendToAllWebSockets(text: frame) }
        try await waitFor { self.rulesFetches() > before }
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(rulesFetches() - before, 1, "four frames in a row share one fetch")
    }

    func testNodeUpdatedOfPlainHostSkipsRules() async throws {
        let rules = attachRules()
        defer { rules.stop() }
        let plain = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "d")
        serveNodes([])
        addHost(plain)
        try await connect()
        try await Task.sleep(nanoseconds: 300_000_000)
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        let remote = try enc.encode(plain.toRemote(global: .defaults))
        let before = rulesFetches()
        server.sendToAllWebSockets(text: #"{"type":"node_updated","node":"# + String(decoding: remote, as: UTF8.self) + "}")
        try await Task.sleep(nanoseconds: 400_000_000)
        XCTAssertEqual(rulesFetches(), before)
    }
}
