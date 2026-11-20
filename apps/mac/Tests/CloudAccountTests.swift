import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudAccountTests: XCTestCase {
    private let token = "user-token-1"
    private var server: LocalTestServer!
    private var keychain: FakeTokenKeychain!
    private var defaults: UserDefaults!
    private var clock = Date(timeIntervalSince1970: 1_000_000)

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token, publicPaths: ["/v1/auth/code", "/v1/auth/code/verify"])
        try server.start()
        keychain = FakeTokenKeychain()
        defaults = UserDefaults(suiteName: "tt-cloud-\(UUID().uuidString)")
    }

    override func tearDown() async throws {
        server.stop()
        server = nil
        try await super.tearDown()
    }

    private var keychainAccount: String { "127.0.0.1:\(server.port)" }

    private func makeAccount() -> CloudAccount {
        CloudAccount(
            endpoint: server.baseURL,
            keychain: keychain,
            client: CloudAuthClient(endpoint: server.baseURL),
            now: { [unowned self] in self.clock },
            retryDelay: .seconds(3600),
            defaults: defaults
        )
    }

    private func serveSignedIn() {
        server.on("POST", "/v1/auth/code") { _ in .empty(202) }
        server.on("POST", "/v1/auth/code/verify") { [token] _ in
            .json(200, Data("{\"token\":\"\(token)\"}".utf8))
        }
        server.on("GET", "/v1/me") { _ in
            .json(200, Data("""
            {"user":{"id":"u1","email":"ana@acme.dev"},"org":{"id":"o1","name":"Acme","plan":"free"},"logins":[]}
            """.utf8))
        }
        server.on("GET", "/v1/billing") { _ in
            .json(200, Data("""
            {"plan":{"id":"free","maxNodes":3,"historyDays":1},"nodes":2,"plans":[]}
            """.utf8))
        }
    }

    func testSendCodePostsEmail() async throws {
        serveSignedIn()
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        let req = try XCTUnwrap(server.captured.first { $0.path == "/v1/auth/code" })
        XCTAssertEqual(String(decoding: req.body, as: UTF8.self), "{\"email\":\"ana@acme.dev\"}")
        XCTAssertEqual(account.status, .codeSent(email: "ana@acme.dev", at: clock))
        XCTAssertEqual(defaults.string(forKey: CloudAccount.emailKey), "ana@acme.dev")
    }

    func testVerifyStoresTokenInKeychain() async throws {
        serveSignedIn()
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        XCTAssertEqual(keychain.items[keychainAccount], token)
        XCTAssertEqual(account.token, token)
        guard case .signedIn(let me) = account.status else { return XCTFail("status: \(account.status)") }
        XCTAssertEqual(me.user.email, "ana@acme.dev")
        XCTAssertEqual(me.org.name, "Acme")
        XCTAssertEqual(account.billing, CloudBilling(plan: CloudPlan(id: "free", maxNodes: 3), nodes: 2))
        let verify = try XCTUnwrap(server.captured.first { $0.path == "/v1/auth/code/verify" })
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: verify.body) as? [String: String])
        XCTAssertEqual(body["want"], "token")
        XCTAssertEqual(body["code"], "482913")
    }

    func testBillingMissingGivesNil() async throws {
        serveSignedIn()
        server.on("GET", "/v1/billing") { _ in .empty(404) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        XCTAssertNil(account.billing)
        guard case .signedIn = account.status else { return XCTFail("status: \(account.status)") }
    }

    func testVerifyWrongCode() async {
        serveSignedIn()
        server.on("POST", "/v1/auth/code/verify") { _ in .empty(401) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "000000")
        XCTAssertEqual(account.status, .failed("The code is not correct."))
        XCTAssertTrue(keychain.items.isEmpty)
    }

    func testVerifyLocked() async {
        serveSignedIn()
        server.on("POST", "/v1/auth/code/verify") { _ in .empty(429) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "000000")
        XCTAssertEqual(account.status, .failed("Too many wrong codes. Send a new code."))
        XCTAssertTrue(keychain.items.isEmpty)
    }

    func testBadEmail() async {
        server.on("POST", "/v1/auth/code") { _ in .empty(400) }
        let account = makeAccount()
        await account.sendCode(email: "nope")
        XCTAssertEqual(account.status, .failed("The email address is not valid."))
        XCTAssertNil(account.pendingEmail)
    }

    func testRestoreSignsOutOn401() async {
        server.on("GET", "/v1/me") { _ in .empty(401) }
        try? keychain.set("old-token", account: keychainAccount)
        let account = makeAccount()
        await account.restore()
        XCTAssertEqual(account.status, .signedOut)
        XCTAssertTrue(keychain.items.isEmpty)
    }

    func testRestoreSignsIn() async {
        serveSignedIn()
        try? keychain.set(token, account: keychainAccount)
        let account = makeAccount()
        await account.restore()
        guard case .signedIn = account.status else { return XCTFail("status: \(account.status)") }
    }

    func testSignOutDeletesToken() async {
        serveSignedIn()
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        account.signOut()
        XCTAssertEqual(account.status, .signedOut)
        XCTAssertNil(account.token)
        XCTAssertTrue(keychain.items.isEmpty)
    }

    func testResendWaits60Seconds() async {
        serveSignedIn()
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        clock.addTimeInterval(30)
        XCTAssertEqual(account.resendWait, 30)
        await account.sendCode(email: "ana@acme.dev")
        XCTAssertEqual(server.captured.filter { $0.path == "/v1/auth/code" }.count, 1)

        clock.addTimeInterval(31)
        XCTAssertEqual(account.resendWait, 0)
        await account.sendCode(email: "ana@acme.dev")
        XCTAssertEqual(server.captured.filter { $0.path == "/v1/auth/code" }.count, 2)
    }

    func testDashboardURLUsesAppHost() {
        let account = CloudAccount(
            endpoint: URL(string: "https://api.towertail.com")!,
            keychain: keychain,
            client: CloudAuthClient(endpoint: server.baseURL)
        )
        XCTAssertEqual(account.dashboardURL.absoluteString, "https://app.towertail.com")
    }
}
