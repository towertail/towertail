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

    func testDashboardLinkUsesHandoff() async throws {
        serveSignedIn()
        server.on("POST", "/v1/auth/handoff") { _ in
            .json(200, Data(#"{"url":"https://api.towertail.com/v1/auth/handoff/callback?code=abc","expiresAt":"2026-01-01T00:00:00Z"}"#.utf8))
        }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        let url = await account.dashboardLink(path: "billing")
        XCTAssertEqual(url.absoluteString, "https://api.towertail.com/v1/auth/handoff/callback?code=abc")
        let req = try XCTUnwrap(server.captured.first { $0.path == "/v1/auth/handoff" })
        XCTAssertEqual(req.headers["authorization"], "Bearer \(token)")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: req.body) as? [String: String])
        XCTAssertEqual(body, ["redirect": "/billing"])
    }

    func testDashboardLinkSendsEmptyRedirectForRoot() async throws {
        serveSignedIn()
        server.on("POST", "/v1/auth/handoff") { _ in .json(200, Data(#"{"url":"https://x.test/c"}"#.utf8)) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        _ = await account.dashboardLink()
        let req = try XCTUnwrap(server.captured.first { $0.path == "/v1/auth/handoff" })
        XCTAssertEqual(String(decoding: req.body, as: UTF8.self), #"{"redirect":""}"#)
    }

    func testDashboardLinkFallsBackOn404() async {
        serveSignedIn()
        server.on("POST", "/v1/auth/handoff") { _ in .json(404, Data(#"{"error":{"message":"not found"}}"#.utf8)) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        let url = await account.dashboardLink(path: "billing")
        XCTAssertEqual(url, account.dashboardURL.appendingPathComponent("billing"))
        let root = await account.dashboardLink()
        XCTAssertEqual(root, account.dashboardURL)
    }

    func testDashboardLinkSignedOut() async {
        let account = makeAccount()
        let url = await account.dashboardLink(path: "billing")
        XCTAssertEqual(url, account.dashboardURL.appendingPathComponent("billing"))
        XCTAssertTrue(server.captured.isEmpty)
    }

    func testOpenDashboardIgnoresSecondCallWhileBusy() async {
        serveSignedIn()
        server.on("POST", "/v1/auth/handoff") { _ in .json(200, Data(#"{"url":"https://x.test/c"}"#.utf8)) }
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        var opened: [URL] = []
        let first = Task { await account.openDashboard(using: { opened.append($0) }) }
        let second = Task { await account.openDashboard(using: { opened.append($0) }) }
        await first.value
        await second.value
        XCTAssertEqual(opened.count, 1)
        XCTAssertEqual(server.captured.filter { $0.path == "/v1/auth/handoff" }.count, 1)
    }

    func testFooterSummary() async {
        serveSignedIn()
        let account = makeAccount()
        XCTAssertNil(PopoverFooter.summary(for: account))
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        XCTAssertEqual(PopoverFooter.summary(for: account), "Free \u{00B7} 2 of 3 hosts")
    }

    private func signIn() async -> CloudAccount {
        serveSignedIn()
        let account = makeAccount()
        await account.sendCode(email: "ana@acme.dev")
        await account.verify(code: "482913")
        return account
    }

    func testDeleteAccountSignsOut() async throws {
        let account = await signIn()
        server.on("DELETE", "/v1/me") { _ in .empty(204) }
        try await account.deleteAccount(email: "ana@acme.dev")
        let req = try XCTUnwrap(server.captured.first { $0.method == "DELETE" })
        XCTAssertEqual(String(decoding: req.body, as: UTF8.self), "{\"email\":\"ana@acme.dev\"}")
        XCTAssertEqual(account.status, .signedOut)
        XCTAssertTrue(keychain.items.isEmpty)
    }

    func testDeleteAccountConflictKeepsToken() async {
        let account = await signIn()
        server.on("DELETE", "/v1/me") { _ in
            .json(409, Data("{\"error\":{\"code\":\"conflict\",\"message\":\"Acme: Make another member an owner first.\"}}".utf8))
        }
        do {
            try await account.deleteAccount(email: "ana@acme.dev")
            XCTFail("no error")
        } catch {
            XCTAssertEqual(error.localizedDescription, "Acme: Make another member an owner first.")
        }
        XCTAssertNotNil(keychain.items[keychainAccount])
        guard case .signedIn = account.status else { return XCTFail("status: \(account.status)") }
    }

    func testAccountDeletionMissingGivesNil() async throws {
        let account = await signIn()
        let found = try await account.deletion()
        XCTAssertNil(found)
    }

    func testAccountDeletionDecodes() async throws {
        let account = await signIn()
        server.on("GET", "/v1/me/deletion") { _ in
            .json(200, Data("""
            {"email":"ana@acme.dev","blocked":true,"orgs":[
            {"id":"\(UUID().uuidString)","name":"Acme","role":"owner","fate":"blocked","members":2,"nodes":4,"reason":"Cancel the subscription first."},
            {"id":"\(UUID().uuidString)","name":"Home","role":"owner","fate":"delete","members":1,"nodes":0}]}
            """.utf8))
        }
        let loaded = try await account.deletion()
        let found = try XCTUnwrap(loaded)
        XCTAssertTrue(found.blocked)
        XCTAssertEqual(found.orgs.map(\.fate), ["blocked", "delete"])
        XCTAssertEqual(found.orgs[0].reason, "Cancel the subscription first.")
        XCTAssertNil(found.orgs[1].reason)
    }

    func testBillingDecodesPayments() throws {
        let with = try JSONDecoder().decode(CloudBilling.self, from: Data("""
        {"plan":{"id":"free","maxNodes":3},"nodes":1,"maxNodes":3,"payments":false}
        """.utf8))
        XCTAssertEqual(with.payments, false)
        let without = try JSONDecoder().decode(CloudBilling.self, from: Data("""
        {"plan":{"id":"free","maxNodes":3},"nodes":1}
        """.utf8))
        XCTAssertNil(without.payments)
        XCTAssertNil(without.maxNodes)
    }

    func testBillingRoom() throws {
        let plan = CloudPlan(id: "free", maxNodes: 3)
        XCTAssertEqual(CloudBilling(plan: plan, nodes: 1, maxNodes: 3).room, 2)
        XCTAssertEqual(CloudBilling(plan: plan, nodes: 5, maxNodes: 3).room, 0)
        XCTAssertNil(CloudBilling(plan: plan, nodes: 1).room)
        XCTAssertFalse(CloudBilling(plan: plan, nodes: 1, payments: false).canBuy)
        XCTAssertTrue(CloudBilling(plan: plan, nodes: 1, payments: true).canBuy)
        XCTAssertTrue(CloudBilling(plan: plan, nodes: 1).canBuy)
    }

    func testRefreshBilling() async throws {
        let account = await signIn()
        XCTAssertEqual(account.billing?.nodes, 2)
        server.on("GET", "/v1/billing") { _ in
            .json(200, Data("""
            {"plan":{"id":"free","maxNodes":3,"historyDays":1},"nodes":3,"maxNodes":3,"plans":[]}
            """.utf8))
        }
        let fresh = await account.refreshBilling()
        XCTAssertEqual(fresh?.nodes, 3)
        XCTAssertEqual(account.billing?.room, 0)

        server.on("GET", "/v1/billing") { _ in .empty(500) }
        let kept = await account.refreshBilling()
        XCTAssertEqual(kept?.nodes, 3)
    }
}
