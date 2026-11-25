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

    /// A fixed window of two hours with a 10 s step.
    private func twoHours(
        _ cache: CloudHistoryCache, _ clock: Clock, _ id: UUID? = nil, metrics: [String] = ["cpu"]
    ) async throws -> RemoteHistoryResponse {
        let to = Date(timeIntervalSince1970: 1_800_000_000)
        return try await cache.nodeHistory(
            id ?? node, metrics: metrics, from: to.addingTimeInterval(-7200), to: to, step: .seconds(10)
        )
    }

    private var historyRequests: Int {
        server.captured.filter { $0.path.hasPrefix("/v1/nodes/\(node.uuidString)/history") }.count
    }

    func testHitWithinTTLDoesNotRefetch() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await twoHours(cache, clock)
        clock.advance(30)
        _ = try await twoHours(cache, clock)
        XCTAssertEqual(historyRequests, 1)
    }

    func testExpiryRefetches() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await twoHours(cache, clock)
        clock.advance(61)
        _ = try await twoHours(cache, clock)
        XCTAssertEqual(historyRequests, 2)
    }

    func testOtherMetricsUseOtherEntry() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await twoHours(cache, clock)
        _ = try await twoHours(cache, clock, metrics: ["mem"])
        XCTAssertEqual(historyRequests, 2)
    }

    func testConcurrentCallsShareOneRequest() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in
            Thread.sleep(forTimeInterval: 0.2)
            return .json(200, body)
        }
        let clock = Clock()
        let cache = makeCache(clock)
        let id = node
        async let a = twoHours(cache, clock, id)
        async let b = twoHours(cache, clock, id)
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
        let clock = Clock()
        let cache = makeCache(clock)
        do {
            _ = try await twoHours(cache, clock)
            XCTFail("expected an error")
        } catch RemoteClient.RemoteError.http(let code, _) {
            XCTAssertEqual(code, 500)
        }
        let ok = try await twoHours(cache, clock)
        XCTAssertEqual(ok.nodeID, node)
        XCTAssertEqual(historyRequests, 2)
    }

    func testTwoHourRangeAsksForTenSecondStep() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let clock = Clock()
        let cache = makeCache(clock)
        _ = try await twoHours(cache, clock, metrics: ["cpu", "mem"])
        let items = URLComponents(string: "http://x" + (server.captured.last?.path ?? ""))?.queryItems
        XCTAssertEqual(items?.first { $0.name == "step" }?.value, "10s")
        XCTAssertEqual(items?.first { $0.name == "metrics" }?.value, "cpu,mem")
    }

    func testWindowKeyRoundsToStep() async throws {
        let body = nodeBody(node)
        server.on("GET", "/v1/nodes/\(node.uuidString)/history") { _ in .json(200, body) }
        let cache = makeCache(Clock())
        let to = Date(timeIntervalSince1970: 1_800_000_001)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], from: to.addingTimeInterval(-3600), to: to, step: .seconds(10))
        let later = to.addingTimeInterval(3)
        _ = try await cache.nodeHistory(node, metrics: ["cpu"], from: later.addingTimeInterval(-3600), to: later, step: .seconds(10))
        XCTAssertEqual(historyRequests, 1)
    }

    // MARK: Process snapshots

    private nonisolated func snapshotBody(_ ts: Date) -> Data {
        let f = ISO8601DateFormatter()
        return Data("""
        {"node_id":"\(node)","ts":"\(f.string(from: ts))","tier":"raw","bucket_s":0,"by":"cpu","total":1,\
        "processes":[{"key":"k.bg","pid":1,"name":"a","cmd":"a","cpu_pct":1,"rss":1}]}
        """.utf8)
    }

    private var snapshotRequests: Int {
        server.captured.filter { $0.path.hasPrefix("/v1/nodes/\(node.uuidString)/processes/at") }.count
    }

    private func snapshotServer() {
        server.on("GET", "/v1/nodes/\(node.uuidString)/processes/at") { req in
            let items = URLComponents(string: "http://x" + req.path)?.queryItems
            let ts = items?.first { $0.name == "ts" }?.value.flatMap { ISO8601DateFormatter().date(from: $0) } ?? Date()
            return .json(200, self.snapshotBody(ts))
        }
    }

    func testProcessesAtLRUEvictsOldestAt200() async throws {
        snapshotServer()
        let clock = Clock()
        let cache = makeCache(clock)
        let first = ProcBucket.of(clock.now().addingTimeInterval(-100_000), now: clock.now())
        for i in 0..<201 {
            _ = try await cache.processesAt(node, bucket: first.shifted(i), by: .cpu)
        }
        XCTAssertEqual(snapshotRequests, 201)
        _ = try await cache.processesAt(node, bucket: first.shifted(200), by: .cpu)
        XCTAssertEqual(snapshotRequests, 201, "the newest entry stays")
        _ = try await cache.processesAt(node, bucket: first, by: .cpu)
        XCTAssertEqual(snapshotRequests, 202, "the oldest entry went out")
    }

    func testClosedBucketNeverExpires() async throws {
        snapshotServer()
        let clock = Clock()
        let cache = makeCache(clock)
        let bucket = ProcBucket.of(clock.now().addingTimeInterval(-7200), now: clock.now())
        _ = try await cache.processesAt(node, bucket: bucket, by: .cpu)
        clock.advance(86_400)
        _ = try await cache.processesAt(node, bucket: bucket, by: .cpu)
        XCTAssertEqual(snapshotRequests, 1)
    }

    func testOpenBucketExpiresAfter10s() async throws {
        snapshotServer()
        let clock = Clock()
        let cache = makeCache(clock)
        let bucket = ProcBucket.of(clock.now(), now: clock.now())
        _ = try await cache.processesAt(node, bucket: bucket, by: .cpu)
        clock.advance(5)
        _ = try await cache.processesAt(node, bucket: bucket, by: .cpu)
        XCTAssertEqual(snapshotRequests, 1)
        clock.advance(6)
        _ = try await cache.processesAt(node, bucket: bucket, by: .cpu)
        XCTAssertEqual(snapshotRequests, 2)
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
