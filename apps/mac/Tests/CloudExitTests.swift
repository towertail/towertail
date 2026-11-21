import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudExitTests: XCTestCase {
    private let token = "user-token-1"
    private var server: LocalTestServer!
    private var nodes: NodeStore!
    private var account: CloudAccount!
    private var exit: CloudExit!
    private var host: Node!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-exit-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("settings.json")
        host = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        nodes = NodeStore(nodes: [host], url: url)

        server.on("GET", "/v1/me") { _ in
            .json(200, Data("""
            {"user":{"id":"u1","email":"ana@acme.dev"},"org":{"id":"o1","name":"Acme","plan":"free"},"logins":[]}
            """.utf8))
        }
        server.on("GET", "/v1/billing") { _ in .empty(404) }
        server.on("DELETE", "/v1/nodes/") { _ in .empty(204) }

        let keychain = FakeTokenKeychain()
        try keychain.set(token, account: "127.0.0.1:\(server.port)")
        account = CloudAccount(
            endpoint: server.baseURL,
            keychain: keychain,
            client: CloudAuthClient(endpoint: server.baseURL),
            retryDelay: .seconds(3600),
            defaults: UserDefaults(suiteName: "tt-exit-\(UUID().uuidString)")!
        )
        await account.restore()

        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let installer = CloudInstaller(
            client: { client },
            nodes: nodes,
            settings: ServerSettings(url: url),
            firstSample: { _, _ in nil },
            connect: { _ in FakeSamplerSession() }
        )
        exit = CloudExit(account: account, installer: installer, nodes: nodes)
    }

    override func tearDown() async throws {
        server.stop()
        try await super.tearDown()
    }

    private func serveDeletion(blocked: Bool) {
        let reason = blocked ? "\"Make another member an owner first.\"" : "null"
        let fate = blocked ? "blocked" : "delete"
        server.on("GET", "/v1/me/deletion") { _ in
            .json(200, Data("""
            {"email":"ana@acme.dev","blocked":\(blocked),"orgs":[{"id":"\(UUID().uuidString)","name":"Acme","role":"owner","fate":"\(fate)","members":1,"nodes":1,"reason":\(reason)}]}
            """.utf8))
        }
    }

    func testLeaveSignsOutOnSuccess() async {
        await exit.leave()
        XCTAssertEqual(exit.phase, .done)
        XCTAssertEqual(account.status, .signedOut)
        XCTAssertNil(nodes.node(withId: host.id)?.cloud)
    }

    func testLeaveStaysSignedInOnFailure() async {
        server.on("DELETE", "/v1/nodes/") { _ in .empty(500) }
        await exit.leave()
        guard case .failed(let text) = exit.phase else { return XCTFail("phase: \(exit.phase)") }
        XCTAssertTrue(text.contains("web"))
        XCTAssertEqual(exit.failures.count, 1)
        guard case .signedIn = account.status else { return XCTFail("status: \(account.status)") }
        XCTAssertNotNil(nodes.node(withId: host.id)?.cloud)
    }

    func testDeleteStopsWhenBlocked() async {
        serveDeletion(blocked: true)
        await exit.deleteAccount(email: "ana@acme.dev")
        guard case .failed(let text) = exit.phase else { return XCTFail("phase: \(exit.phase)") }
        XCTAssertTrue(text.contains("Make another member an owner first."))
        XCTAssertFalse(server.captured.contains { $0.method == "DELETE" })
        XCTAssertNotNil(nodes.node(withId: host.id)?.cloud)
    }

    func testDeleteMovesHostsThenDeletes() async throws {
        serveDeletion(blocked: false)
        server.on("DELETE", "/v1/me") { _ in .empty(204) }
        await exit.deleteAccount(email: "ana@acme.dev")

        XCTAssertEqual(exit.phase, .done)
        XCTAssertEqual(account.status, .signedOut)
        let order = server.captured.filter { $0.path.hasPrefix("/v1/me/deletion") || $0.method == "DELETE" }
            .map { "\($0.method) \($0.path)" }
        XCTAssertEqual(order, ["GET /v1/me/deletion", "DELETE /v1/nodes/\(host.id.uuidString)", "DELETE /v1/me"])
        let delete = try XCTUnwrap(server.captured.last { $0.path == "/v1/me" && $0.method == "DELETE" })
        XCTAssertEqual(String(decoding: delete.body, as: UTF8.self), "{\"email\":\"ana@acme.dev\"}")
    }

    func testDeleteStopsWhenHostsStay() async {
        serveDeletion(blocked: false)
        server.on("DELETE", "/v1/nodes/") { _ in .empty(500) }
        await exit.deleteAccount(email: "ana@acme.dev")
        XCTAssertFalse(server.captured.contains { $0.method == "DELETE" && $0.path == "/v1/me" })
        guard case .signedIn = account.status else { return XCTFail("status: \(account.status)") }
    }

    func testDeleteOnOldCloud() async {
        await exit.deleteAccount(email: "ana@acme.dev")
        XCTAssertEqual(exit.phase, .failed("This Cloud server cannot delete accounts yet."))
        XCTAssertFalse(server.captured.contains { $0.method == "DELETE" })
    }
}
