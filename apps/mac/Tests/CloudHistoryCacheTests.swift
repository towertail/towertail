import XCTest
@testable import Towertail

@available(macOS 14.0, *)
@MainActor
final class CloudHistoryCacheTests: XCTestCase {
    private var server: LocalTestServer!
    private let token = "test-token"
    private let node = UUID()

    /// A clock that tests move.
    private final class Clock: @unchecked Sendable {
        private let lock = NSLock()
        private var t = Date(timeIntervalSince1970: 1_800_000_000)
        func now() -> Date { lock.withLock { t } }
        func advance(_ s: TimeInterval) { lock.withLock { t = t.addingTimeInterval(s) } }
    }

    override func setUp() async throws {
        try await super.setUp()
        server = try LocalTestServer(token: token)
        try server.start()
    }

    override func tearDown() async throws {
        server.stop()
        server = nil
        try await super.tearDown()
    }

    private func nodeBody(_ id: UUID) -> Data {
        Data("""
        {"node_id":"\(id)","series":{"cpu":[{"ts":"2026-04-20T12:00:00Z","value":50}]},\
        "from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"raw"}
        """.utf8)
    }

    private func makeCache(_ clock: Clock) -> CloudHistoryCache {
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        return CloudHistoryCache(client: client, ttl: .seconds(60), now: { clock.now() })
    }

    private var historyRequests: Int {
        server.captured.filter { $0.path.hasPrefix("/v1/nodes/\(node.uuidString)/history") }.count
    }

    func testHitWithinTTLDoesNotRefetch() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        clock.advance(30)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        XCTAssertEqual(historyRequests, 1)
    }

    func testExpiryRefetches() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        clock.advance(61)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        XCTAssertEqual(historyRequests, 2)
    }

    func testOtherMetricsUseOtherEntry() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let cache = makeCache(Clock())
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        _ = try await cache.nodeHistory(node, metrics: ["mem"], range: 7200)
        XCTAssertEqual(historyRequests, 2)
    }

    func testConcurrentCallsShareOneRequest() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in
            Thread.sleep(forTimeInterval: 0.2)
            return .json(200, body)
        }
        let cache = makeCache(Clock())
        let id = node
        async let a = cache.nodeHistory(id, metrics: ["cpu"], range: 7200)
        async let b = cache.nodeHistory(id, metrics: ["cpu"], range: 7200)
        _ = try await (a, b)
        XCTAssertEqual(historyRequests, 1)
    }

    func testErrorIsNotCached() async throws {
        let body = nodeBody(node)
        let calls = Counter()
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in
            calls.next() == 1
                ? .json(500, Data(#"{"error":{"code":"boom","message":"kaput"}}"#.utf8))
                : .json(200, body)
        }
        let cache = makeCache(Clock())
        do {
            _ = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
            XCTFail("expected an error")
        } catch RemoteClient.RemoteError.http(let code, _) {
            XCTAssertEqual(code, 500)
        }
        let ok = try await cache.nodeHistory(node, metrics: ["cpu"], range: 7200)
        XCTAssertEqual(ok.nodeID, node)
        XCTAssertEqual(historyRequests, 2)
    }

    func testTwoHourRangeAsksForTenSecondStep() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let cache = makeCache(Clock())
        _ = try await cache.nodeHistory(node, metrics: ["cpu", "mem"], range: 7200)
        let items = URLComponents(string: "http://x" + (server.captured.last?.path ?? ""))?.queryItems
        XCTAssertEqual(items?.first { $0.name == "step" }?.value, "10s")
        XCTAssertEqual(items?.first { $0.name == "metrics" }?.value, "cpu,mem")
    }

    func testFleetSplitsAt200Nodes() async throws {
        server.on("GET", "/v1/history") { req in
            let items = URLComponents(string: "http://x" + req.path)?.queryItems
            let ids = (items?.first { $0.name == "nodes" }?.value ?? "").split(separator: ",")
            let nodes = ids.map { #"{"node_id":"\#($0)","series":{"cpu":[{"ts":"2026-04-20T12:00:00Z","value":1}]}}"# }
            return .json(200, Data("""
            {"from":"2026-04-20T11:30:00Z","to":"2026-04-20T12:00:00Z","resolution":"raw","nodes":[\(nodes.joined(separator: ","))]}
            """.utf8))
        }
        let cache = makeCache(Clock())
        let ids = (0..<250).map { _ in UUID() }
        let fleet = try await cache.fleet(ids, range: 1800)
        XCTAssertEqual(server.captured.filter { $0.path.hasPrefix("/v1/history") }.count, 2)
        XCTAssertEqual(fleet.nodes.count, 250)
    }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func next() -> Int { lock.withLock { n += 1; return n } }
}
