import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudSessionTests: XCTestCase {
    private let token = "user-token-1"
    private var server: LocalTestServer!
    private var nodes: NodeStore!
    private var session: CloudSession!
    private var account: CloudAccount!
    private var linked: Node!
    private var plain: Node!
    private var cloudOnly: Node!
    private var released: ReleasedHosts!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("tt-session-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("settings.json")
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        linked = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        plain = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "d")
        cloudOnly = Node(displayName: "edge", kind: .cloud, cloud: CloudLink(enrolledAt: Date()))
        nodes = NodeStore(nodes: [linked, plain, cloudOnly], url: url)

        let remote = try JSONEncoder().encode([linked!, cloudOnly!].map { $0.toRemote(global: .defaults) })
        server.on("GET", "/v1/me") { _ in
            .json(200, Data("""
            {"user":{"id":"u1","email":"ana@acme.dev"},"org":{"id":"o1","name":"Acme","plan":"free"},"logins":[]}
            """.utf8))
        }
        server.on("GET", "/v1/billing") { _ in .empty(404) }
        server.on("GET", "/v1/nodes") { _ in .json(200, remote) }
        server.on("GET", "/v1/alerts") { _ in .json(200, Data("{\"alerts\":[]}".utf8)) }

        let keychain = FakeTokenKeychain()
        try keychain.set(token, account: "127.0.0.1:\(server.port)")
        account = CloudAccount(
            endpoint: server.baseURL,
            keychain: keychain,
            client: CloudAuthClient(endpoint: server.baseURL),
            retryDelay: .seconds(3600),
            defaults: UserDefaults(suiteName: "tt-session-\(UUID().uuidString)")!
        )
        released = ReleasedHosts(defaults: UserDefaults(suiteName: "tt-session-\(UUID().uuidString)")!)
        session = CloudSession(account: account, servers: ServerStore(), nodes: nodes, released: released)
        await account.restore()
    }

    override func tearDown() async throws {
        account.signOut()
        server.stop()
        try await super.tearDown()
    }

    func testSignOutClearsSSHLinks() {
        XCTAssertNotNil(nodes.node(withId: linked.id)?.cloud)
        account.signOut()
        let stored = nodes.node(withId: linked.id)
        XCTAssertNotNil(stored)
        XCTAssertNil(stored?.cloud)
        XCTAssertEqual(stored?.kind, .ssh)
        XCTAssertNotNil(nodes.node(withId: plain.id))
    }

    func testSignOutRecordsReleasedHosts() {
        account.signOut()
        XCTAssertEqual(released.ids, [linked.id], "only the SSH host with a link")
    }

    func testReleasedHostsAddKeepsOldIds() {
        let store = ReleasedHosts(defaults: UserDefaults(suiteName: "tt-released-\(UUID().uuidString)")!)
        let a = UUID(), b = UUID()
        store.add([a])
        store.add([b])
        XCTAssertEqual(store.ids, [a, b])
        store.retain([b])
        XCTAssertEqual(store.ids, [b])
    }

    func testSignOutRemovesCloudOnlyNodes() {
        XCTAssertNotNil(nodes.node(withId: cloudOnly.id))
        account.signOut()
        XCTAssertNil(nodes.node(withId: cloudOnly.id))
    }

    func testSignOutStopsFeed() {
        XCTAssertNotNil(session.feed)
        XCTAssertNotNil(session.client)
        account.signOut()
        XCTAssertNil(session.feed)
        XCTAssertNil(session.client)
    }

    func testSignInGivesHistoryLoader() {
        XCTAssertNotNil(session.history)
        account.signOut()
        XCTAssertNil(session.history)
    }

    func testLocalBackendUsesSessionHistory() {
        let backend = LocalBackend()
        XCTAssertNil(backend.cloudHistory)
        backend.cloud = session
        XCTAssertTrue(backend.cloudHistory === session.history)
        account.signOut()
        XCTAssertNil(backend.cloudHistory)
    }
}
