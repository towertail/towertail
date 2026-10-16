import XCTest
@testable import Towertail

/// Remote disk scans against LocalTestServer: real HTTP and WebSocket.
@available(macOS 14.0, *)
@MainActor
final class RemoteDiskScanTests: XCTestCase {
    /// Server side state of the scan job. The handlers read it on the
    /// server queue.
    final class ScanState: @unchecked Sendable {
        struct Fields {
            var job: DiskScanJob
            var startStatus = 202
            var startBody: Data?
            var result: Data?
            var resultHeaders: [String: String] = ["Content-Encoding": "gzip"]
            var jobs: [DiskScanJob] = []
        }

        private let lock = NSLock()
        private var fields: Fields

        init(job: DiskScanJob) { fields = Fields(job: job) }

        var job: DiskScanJob {
            get { with { $0.job } }
            set { with { $0.job = newValue } }
        }

        /// Not reentrant: do not use `job` inside `f`.
        func with<T>(_ f: (inout Fields) -> T) -> T { lock.withLock { f(&fields) } }
    }

    private let token = "scan-token"
    private var server: LocalTestServer!
    private var backend: RemoteBackend!
    private var state: ScanState!
    private var node: Node!
    private var dbURL: URL!

    override func setUp() async throws {
        server = try LocalTestServer(token: token)
        try server.start()
        let nodeID = UUID()
        node = Node(id: nodeID, displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1", allowDiskScan: true)
        state = ScanState(job: DiskScanJob(id: "job-1", nodeId: nodeID.uuidString.lowercased(), state: .queued,
                                           root: "/", createdTs: Date(timeIntervalSince1970: 1_800_000_000)))
        dbURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-remote-scan-\(UUID().uuidString).sqlite")
        try register()
        backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        try await waitFor { self.backend.nodes.nodes.count == 1 }
        try await waitFor { self.server.hasWebSocketClient }
    }

    override func tearDown() async throws {
        backend?.stop()
        server?.stop()
        if let dbURL { try? FileManager.default.removeItem(at: dbURL) }
    }

    private func register() throws {
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        let nodes = try enc.encode([node.toRemote(global: .defaults)])
        server.on("GET", "/v1/nodes") { _ in .json(200, nodes) }
        let settings = try enc.encode(RemoteServerSettings(
            thresholds: .defaults, localPollingIntervalSeconds: 2, sshPollingIntervalSeconds: 10,
            notificationsEnabled: true, notifyWarn: true, notifyCritical: true, notifyDebounceSeconds: 90,
            autoUpdateSamplersEnabled: true, postWakeGraceSeconds: 15))
        server.on("GET", "/v1/settings") { _ in .json(200, settings) }
        let state = self.state!
        let id = node.id.uuidString
        server.on("POST", "/v1/nodes/\(id)/disk-scans") { req in
            state.with { s in
                s.startBody = req.body
                switch s.startStatus {
                case 403:
                    return .json(403, Data(#"{"error":{"code":"forbidden","message":"disk scans are off for this node"}}"#.utf8))
                case 409:
                    let job = String(decoding: try! JSONEncoder().encode(s.job), as: UTF8.self)
                    return .json(409, Data(#"{"error":{"code":"conflict","message":"a scan runs"},"job":\#(job)}"#.utf8))
                default:
                    return .json(202, try! JSONEncoder().encode(s.job))
                }
            }
        }
        server.on("GET", "/v1/nodes/\(id)/disk-scans") { _ in
            let jobs = state.with { $0.jobs }
            return .json(200, try! JSONEncoder().encode(["jobs": jobs]))
        }
        server.on("GET", "/v1/disk-scans/job-1") { _ in .json(200, try! JSONEncoder().encode(state.job)) }
        server.on("POST", "/v1/disk-scans/job-1/cancel") { _ in
            var j = state.job
            j.cancelRequested = true
            state.job = j
            return .json(200, try! JSONEncoder().encode(j))
        }
        server.on("GET", "/v1/disk-scans/job-1/result") { _ in
            state.with { s in
                guard let r = s.result else { return .json(404, Data(#"{"error":{"code":"not_found","message":"no result"}}"#.utf8)) }
                var resp = LocalTestServer.Response.json(200, r)
                resp.headers = s.resultHeaders
                return resp
            }
        }
    }

    // MARK: Helpers

    /// Collects the events of a scan stream.
    final class Collector {
        var events: [DiskScanEvent] = []
        var error: Error?
        var finished = false
        var task: Task<Void, Never>?
    }

    private func collect(_ stream: AsyncThrowingStream<DiskScanEvent, Error>) -> Collector {
        let c = Collector()
        c.task = Task { @MainActor in
            do {
                for try await e in stream { c.events.append(e) }
            } catch {
                c.error = error
            }
            c.finished = true
        }
        return c
    }

    private func scanner(poll: TimeInterval = 60) throws -> RemoteDiskScanner {
        var s = try XCTUnwrap(backend.diskScanner(for: node) as? RemoteDiskScanner)
        s.pollInterval = poll
        return s
    }

    private func push(_ job: DiskScanJob) {
        state.job = job
        let body = String(decoding: try! JSONEncoder().encode(job), as: UTF8.self)
        server.sendToAllWebSockets(text: #"{"type":"disk_scan_updated","nodeId":"\#(node.id.uuidString.lowercased())","job":\#(body)}"#)
    }

    private func job(_ state: DiskScanJob.State, progress: DiskScanProgress? = nil, error: String = "",
                     resultBytes: Int64 = 0) -> DiskScanJob {
        var j = self.state.job
        j.state = state
        j.progress = progress
        j.error = error
        j.resultBytes = resultBytes
        if state != .queued { j.startedTs = Date(timeIntervalSince1970: 1_800_000_004) }
        if state.isFinal { j.finishedTs = Date(timeIntervalSince1970: 1_800_000_052) }
        return j
    }

    private func gzip(_ data: Data) throws -> Data {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/gzip")
        p.arguments = ["-c"]
        let input = Pipe(), output = Pipe()
        p.standardInput = input
        p.standardOutput = output
        try p.run()
        input.fileHandleForWriting.write(data)
        try input.fileHandleForWriting.close()
        let out = output.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return out
    }

    private func fixtureResult() throws -> (DiskScanResult, Data) {
        let data = try XCTUnwrap(ScanFixtures.lines("linux").last)
        guard case .result(let r) = try DiskScanEvent.decodeLine(data) else { throw XCTSkip("no result line") }
        return (r, data)
    }

    private func waitFor(timeout: TimeInterval = 5, _ cond: @escaping @MainActor () -> Bool) async throws {
        let end = Date().addingTimeInterval(timeout)
        while !cond() {
            if Date() > end { return XCTFail("condition not met") }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
    }

    private func states(_ c: Collector) -> [DiskScanJob.State] {
        c.events.compactMap { if case .job(let j) = $0 { return j.state } else { return nil } }
    }

    // MARK: Tests

    func testStartProgressDoneDownloadsResult() async throws {
        let (r, line) = try fixtureResult()
        state.with { $0.result = try! self.gzip(line) }
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest(root: "/")))
        try await waitFor { self.states(c).contains(.queued) }
        let body = try XCTUnwrap(state.with { $0.startBody })
        XCTAssertEqual(try JSONDecoder().decode(DiskScanRequest.self, from: body), DiskScanRequest(root: "/"))

        push(job(.running, progress: DiskScanProgress(files: 10, bytes: 5_000)))
        try await waitFor { self.states(c).contains(.running) }
        let running = c.events.compactMap { if case .job(let j) = $0, j.state == .running { return j } else { return nil } }.first
        XCTAssertEqual(running?.progress?.files, 10)

        push(job(.done, resultBytes: 1_800_000))
        try await waitFor { c.finished }
        XCTAssertNil(c.error)
        XCTAssertEqual(c.events.last, .result(r))
    }

    func testResultWithoutContentEncodingIsDecoded() async throws {
        let (r, line) = try fixtureResult()
        state.with {
            $0.result = try! self.gzip(line)
            $0.resultHeaders = ["Content-Type": "application/octet-stream"]
        }
        state.job = job(.done, resultBytes: 10)
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest(root: "/")))
        try await waitFor { c.finished }
        XCTAssertNil(c.error)
        XCTAssertEqual(c.events.last, .result(r))
    }

    func testDoneWithoutResultFails() async throws {
        state.job = job(.done)
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest()))
        try await waitFor { c.finished }
        XCTAssertEqual(c.error as? DiskScanError, .remote("The server has no result for this scan."))
    }

    func testCancelAsksServer() async throws {
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest()))
        try await waitFor { self.states(c).contains(.queued) }
        push(job(.running))
        try await waitFor { self.states(c).contains(.running) }
        c.task?.cancel()
        try await waitFor { self.server.captured.contains { $0.method == "POST" && $0.path == "/v1/disk-scans/job-1/cancel" } }
        try await waitFor { c.finished }
    }

    func testFailedJobGivesError() async throws {
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest()))
        try await waitFor { self.states(c).contains(.queued) }
        let msg = "disk scans are off on this host: run the sampler with --allow-disk-scan"
        push(job(.failed, error: msg))
        try await waitFor { c.finished }
        XCTAssertEqual(c.events.last, .error(msg))
    }

    func testForbiddenGivesScansOff() async throws {
        state.with { $0.startStatus = 403 }
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest()))
        try await waitFor { c.finished }
        XCTAssertEqual(c.error as? DiskScanError, .scansOff("Disk scans are off for this node."))
    }

    func testConflictFollowsRunningJob() async throws {
        let (r, line) = try fixtureResult()
        state.with { $0.startStatus = 409; $0.result = try! self.gzip(line) }
        state.job = job(.running, progress: DiskScanProgress(files: 3))
        let c = collect(try scanner().scan(node: node, request: DiskScanRequest()))
        try await waitFor { self.states(c).contains(.running) }
        push(job(.done, resultBytes: 100))
        try await waitFor { c.finished }
        XCTAssertNil(c.error)
        XCTAssertEqual(c.events.last, .result(r))
    }

    func testLostWebSocketRefetchesJob() async throws {
        let (r, line) = try fixtureResult()
        state.with { $0.result = try! self.gzip(line) }
        let c = collect(try scanner(poll: 600).scan(node: node, request: DiskScanRequest()))
        try await waitFor { self.states(c).contains(.queued) }
        push(job(.running))
        try await waitFor { self.states(c).contains(.running) }

        server.closeAllWebSockets()
        // The job ends while the client has no WebSocket.
        state.job = job(.done, resultBytes: 100)
        try await waitFor(timeout: 10) { self.server.hasWebSocketClient }
        // The first frame of the new connection starts the refetch.
        server.sendToAllWebSockets(text: #"{"type":"pong"}"#)
        try await waitFor(timeout: 10) { c.finished }
        XCTAssertNil(c.error)
        XCTAssertEqual(c.events.last, .result(r))
    }

    func testPollFindsEndWithoutWebSocket() async throws {
        let c = collect(try scanner(poll: 0.1).scan(node: node, request: DiskScanRequest()))
        try await waitFor { self.states(c).contains(.queued) }
        state.job = job(.cancelled)
        try await waitFor { c.finished }
        XCTAssertEqual(c.events.last, .cancelled)
    }

    // MARK: Model

    func testModelFollowsJobFromOtherDevice() async throws {
        let (r, line) = try fixtureResult()
        let running = job(.running, progress: DiskScanProgress(files: 7))
        state.job = running
        state.with { $0.jobs = [running]; $0.result = try! self.gzip(line) }
        let store = DiskScanStore(url: dbURL)
        let m = DiskUsageModel(node: node, store: store, scanner: try scanner(), scope: "~")
        let attach = Task { await m.attach() }
        defer { attach.cancel() }
        try await waitFor { m.isRunning && m.job?.state == .running }
        XCTAssertEqual(m.scope, "/")
        XCTAssertEqual(m.progress?.files, 7)
        push(job(.done, resultBytes: 100))
        try await waitFor { m.phase == .done }
        XCTAssertEqual(m.result, r)
        XCTAssertEqual(store.latest(nodeID: node.id, scope: "/")?.result, r)
    }

    func testModelSeesNewJobWhileOpen() async throws {
        let store = DiskScanStore(url: dbURL)
        let m = DiskUsageModel(node: node, store: store, scanner: try scanner(), scope: "/")
        let attach = Task { await m.attach() }
        defer { attach.cancel() }
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertFalse(m.isRunning)
        push(job(.running))
        try await waitFor { m.isRunning }
        m.cancel()
        try await waitFor { self.server.captured.contains { $0.path == "/v1/disk-scans/job-1/cancel" } }
    }

    func testModelForbiddenAndSamplerOff() async throws {
        let store = DiskScanStore(url: dbURL)
        let m = DiskUsageModel(node: node, store: store, scanner: try scanner(), scope: "/")
        state.with { $0.startStatus = 403 }
        m.start()
        try await waitFor { !m.isRunning }
        XCTAssertEqual(m.scansOff, "Disk scans are off for this node.")

        state.with { $0.startStatus = 202 }
        state.job = job(.queued)
        m.start()
        try await waitFor { m.job != nil && m.isRunning }
        XCTAssertNil(m.scansOff)
        let msg = "disk scans are off on this host: run the sampler with --allow-disk-scan"
        push(job(.failed, error: msg))
        try await waitFor { m.phase == .failed(msg) }
        XCTAssertEqual(m.scansOff, msg)
    }

    // MARK: Wire

    func testNodeAllowDiskScanRoundTrip() throws {
        let json = #"{"id":"\#(UUID().uuidString)","displayName":"a","kind":"ssh","tags":[],"enabled":true,"iconOnWarn":true,"iconOnCritical":true,"notifyOnWarn":true,"notifyOnCritical":true,"favorite":false}"#
        let old = try JSONDecoder().decode(RemoteNode.self, from: Data(json.utf8))
        XCTAssertNil(old.allowDiskScan)
        XCTAssertFalse(Node(remote: old).allowDiskScan)
        var n = Node(remote: old)
        n.allowDiskScan = true
        let out = try JSONSerialization.jsonObject(with: JSONEncoder().encode(n.toRemote(global: .defaults))) as? [String: Any]
        XCTAssertEqual(out?["allowDiskScan"] as? Bool, true)
        let local = try JSONDecoder().decode(Node.self, from: JSONEncoder().encode(n))
        XCTAssertTrue(local.allowDiskScan)
    }

    func testStripSteps() {
        let done = job(.done, resultBytes: 1_800_000)
        let steps = RemoteJobStrip.steps(done)
        XCTAssertEqual(steps.map(\.label), ["Queued", "Sampler picked up", "Scanned", "Result in"])
        XCTAssertTrue(steps.allSatisfy { $0.mark == .done })
        XCTAssertEqual(steps[2].value, UsageText.duration(48_000))
        XCTAssertEqual(steps[3].value, "1.8 MB")
        XCTAssertEqual(RemoteJobStrip.steps(job(.queued)).map(\.mark), [.done, .pending, .pending, .pending])
        XCTAssertEqual(RemoteJobStrip.steps(job(.failed)).last?.label, "Failed")
        XCTAssertEqual(RemoteJobStrip.steps(job(.cancelled)).last?.mark, .failed)
    }
}
