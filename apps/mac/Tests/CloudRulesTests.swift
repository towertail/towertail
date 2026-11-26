import XCTest
@testable import Towertail

@MainActor
final class CloudRulesTests: XCTestCase {
    private let token = "cloud-user-token"
    private let hostA = UUID(uuidString: "22222222-2222-2222-2222-222222222222")!
    private var server: LocalTestServer!
    private var client: RemoteClient!
    private var cacheURL: URL!

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
        client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        cacheURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("tt-rules-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("cloud-rules.json")
    }

    override func tearDown() async throws {
        server.stop()
        try? FileManager.default.removeItem(at: cacheURL.deletingLastPathComponent())
        try await super.tearDown()
    }

    private func fixtureData() throws -> Data {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "rules-view", withExtension: "json"))
        return try Data(contentsOf: url)
    }

    private func makeRules(gap: TimeInterval = 0.05) -> CloudRules {
        CloudRules(client: client, device: "test-mac", cache: CloudRulesCache(url: cacheURL), gap: gap)
    }

    private func count(_ method: String, _ path: String) -> Int {
        server.captured.filter { $0.method == method && $0.path == path }.count
    }

    func testRefreshFillsEffective() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        let rules = makeRules()
        XCTAssertNil(rules.effective(for: hostA))
        await rules.refresh()
        XCTAssertEqual(rules.effective(for: hostA)?.alerts.cpu.sustainSeconds, 60)
        XCTAssertEqual(rules.org?.longProc.cpuPct, 50)
        XCTAssertEqual(rules.host(hostA)?.ignoredProcs, ["pg_dump"])
        XCTAssertNil(rules.error)
        XCTAssertNil(rules.effective(for: UUID()))
    }

    func testFailedRefreshKeepsLastRules() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        let rules = makeRules()
        await rules.refresh()
        server.on("GET", "/v1/rules") { _ in .json(500, Data("{}".utf8)) }
        await rules.refresh()
        XCTAssertNotNil(rules.error)
        XCTAssertNotNil(rules.effective(for: hostA))
    }

    func testCacheLoadsBeforeFirstFetch() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        await makeRules().refresh()
        let second = makeRules()
        XCTAssertNotNil(second.effective(for: hostA), "the cache fills the rules before a fetch")
        XCTAssertEqual(second.org?.thresholds.cpuWarn, 0.8)
    }

    func testSaveHostStampsMacOrigin() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        server.on("PUT", "/v1/nodes/\(hostA.uuidString)/rules") { _ in .json(200, Data("{}".utf8)) }
        let rules = makeRules()
        let body = RemoteHostRules(
            thresholds: ThresholdOverrides(cpu: ThresholdPair(warn: 0.5, critical: 0.7)),
            origin: RemoteRulesOrigin(client: "web")
        )
        try await rules.saveHost(hostA, body)
        let put = try XCTUnwrap(server.captured.first { $0.method == "PUT" })
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: put.body) as? [String: Any])
        let origin = try XCTUnwrap(json["origin"] as? [String: Any])
        XCTAssertEqual(origin["client"] as? String, "mac")
        XCTAssertEqual(origin["device"] as? String, "test-mac")
        XCTAssertNotNil(json["thresholds"])
        XCTAssertEqual(json["muted"] as? [String], [])
        XCTAssertEqual(count("GET", "/v1/rules"), 1, "a save reads the rules back")
    }

    func testMuteBodies() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        server.on("PUT", "/v1/mutes") { _ in .json(200, Data("{}".utf8)) }
        let rules = makeRules()
        try await rules.setMute(.cpu, nodeID: hostA, muted: true)
        try await rules.setLongMute(nodeID: nil, muted: false)
        let puts = server.captured.filter { $0.method == "PUT" }
        XCTAssertEqual(puts.count, 2)
        let first = try XCTUnwrap(JSONSerialization.jsonObject(with: puts[0].body) as? [String: Any])
        XCTAssertEqual(first["metric"] as? String, "cpu")
        XCTAssertEqual(first["nodeId"] as? String, hostA.uuidString)
        XCTAssertEqual(first["muted"] as? Bool, true)
        let second = try XCTUnwrap(JSONSerialization.jsonObject(with: puts[1].body) as? [String: Any])
        XCTAssertEqual(second["metric"] as? String, "long")
        XCTAssertEqual(second["muted"] as? Bool, false)
    }

    func testForbiddenGivesPlainMessage() async throws {
        server.on("PUT", "/v1/nodes/\(hostA.uuidString)/rules") { _ in .json(403, Data("{}".utf8)) }
        let rules = makeRules()
        do {
            try await rules.saveHost(hostA, RemoteHostRules())
            XCTFail("expected a 403 error")
        } catch let e as RemoteClient.RemoteError {
            guard case .forbidden = e else { return XCTFail("wrong error: \(e)") }
        }
    }

    func testRefreshSoonCoalescesCalls() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        let rules = makeRules(gap: 0.2)
        await rules.refresh()
        for _ in 0..<5 { rules.refreshSoon() }
        try await waitFor { self.count("GET", "/v1/rules") >= 2 }
        try await Task.sleep(nanoseconds: 400_000_000)
        XCTAssertEqual(count("GET", "/v1/rules"), 2)
    }

    func testStopCancelsPendingFetch() async throws {
        let data = try fixtureData()
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        let rules = makeRules(gap: 0.3)
        await rules.refresh()
        rules.refreshSoon()
        rules.stop()
        try await Task.sleep(nanoseconds: 600_000_000)
        XCTAssertEqual(count("GET", "/v1/rules"), 1)
    }
}
