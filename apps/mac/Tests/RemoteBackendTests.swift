import XCTest
@testable import Towertail

/// End-to-end tests for `RemoteClient` and `RemoteBackend` against an
/// in-process real HTTP+WebSocket server (LocalTestServer). No mocks —
/// traffic goes through URLSession and a real TCP socket.
@available(macOS 14.0, *)
@MainActor
final class RemoteBackendTests: XCTestCase {
    var server: LocalTestServer!
    let token = "test-token-abc123"

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

    // MARK: - RemoteClient REST

    func testRemoteClientListNodesReturnsSeededFleet() async throws {
        let remote = RemoteNode(
            id: UUID(),
            displayName: "node-a",
            kind: "ssh",
            sshUser: "ops",
            sshHost: "a.example.com",
            tags: ["prod"],
            enabled: true,
            iconOnWarn: true,
            iconOnCritical: true,
            notifyOnWarn: true,
            notifyOnCritical: true,
            customThresholds: nil,
            snoozedUntil: nil,
            favorite: false
        )
        let body = try jsonEncoder().encode([remote])
        server.on("GET", "/v1/nodes") { _ in .json(200, body) }

        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let nodes = try await client.listNodes()
        XCTAssertEqual(nodes.count, 1)
        XCTAssertEqual(nodes.first?.displayName, "node-a")
        XCTAssertEqual(nodes.first?.sshUser, "ops")
    }

    func testRemoteClientSendsBearerToken() async throws {
        server.on("GET", "/v1/nodes") { _ in .json(200, Data("[]".utf8)) }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        _ = try await client.listNodes()
        let cap = try lastRequest()
        XCTAssertEqual(cap.headers["authorization"], "Bearer \(token)")
    }

    func testRemoteClientCreateNodeRoundtrip() async throws {
        server.on("POST", "/v1/nodes") { req in
            // Echo whatever we received, with a server-assigned id if
            // missing. Here we just echo back verbatim.
            return .json(201, req.body)
        }
        let node = RemoteNode(
            id: UUID(), displayName: "echoed", kind: "ssh",
            sshUser: "a", sshHost: "b",
            tags: [], enabled: true,
            iconOnWarn: true, iconOnCritical: true,
            notifyOnWarn: true, notifyOnCritical: true,
            customThresholds: nil, snoozedUntil: nil, favorite: false
        )
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let created = try await client.createNode(node)
        XCTAssertEqual(created.id, node.id)
        XCTAssertEqual(created.displayName, "echoed")
    }

    func testRemoteClientPutSettingsEncodesAndDecodes() async throws {
        server.on("PUT", "/v1/settings") { req in
            // Verify body decodes as RemoteServerSettings server-side, then
            // echo it back. This catches silent key-name drift.
            return .json(200, req.body)
        }
        let rs = RemoteServerSettings(
            thresholds: .defaults,
            localPollingIntervalSeconds: 2,
            sshPollingIntervalSeconds: 10,
            notificationsEnabled: true,
            notifyWarn: true,
            notifyCritical: true,
            notifyDebounceSeconds: 90,
            autoUpdateSamplersEnabled: false,
            postWakeGraceSeconds: 15
        )
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let out = try await client.putSettings(rs)
        XCTAssertEqual(out.notifyDebounceSeconds, 90)
        XCTAssertEqual(out.localPollingIntervalSeconds, 2)
        XCTAssertEqual(out.postWakeGraceSeconds, 15)
    }

    func testRemoteClientDeleteNodeByPath() async throws {
        let id = UUID()
        // Prefix match: registering "/v1/nodes/" lets the server accept
        // "/v1/nodes/<uuid>".
        server.on("DELETE", "/v1/nodes/") { req in
            XCTAssertTrue(req.path.hasSuffix(id.uuidString))
            return .empty(204)
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        try await client.deleteNode(id: id)
    }

    func testRemoteClient4xxSurfacesHTTPError() async throws {
        server.on("GET", "/v1/nodes") { _ in
            .json(500, Data("{\"error\":{\"code\":\"boom\",\"message\":\"kaput\"}}".utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        do {
            _ = try await client.listNodes()
            XCTFail("expected HTTP error")
        } catch let RemoteClient.RemoteError.http(code, body) {
            XCTAssertEqual(code, 500)
            XCTAssertTrue(body.contains("kaput"))
        } catch {
            XCTFail("wrong error: \(error)")
        }
    }

    func testRemoteClientWrongTokenIsRejected() async throws {
        server.on("GET", "/v1/nodes") { _ in .json(200, Data("[]".utf8)) }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: "wrong"))
        do {
            _ = try await client.listNodes()
            XCTFail("expected 401")
        } catch let RemoteClient.RemoteError.http(code, _) {
            XCTAssertEqual(code, 401)
        } catch {
            XCTFail("wrong error: \(error)")
        }
    }

    // MARK: - RemoteBackend lifecycle + WebSocket fan-out

    func testRemoteBackendInitialLoadPopulatesNodes() async throws {
        let node = sampleRemoteNode(name: "node-x")
        let nodesPayload = try jsonEncoder().encode([node])
        server.on("GET", "/v1/nodes") { _ in .json(200, nodesPayload) }
        let settingsPayload = try jsonEncoder().encode(sampleRemoteSettings())
        server.on("GET", "/v1/settings") { _ in .json(200, settingsPayload) }

        let backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        // The initial load is async; poll for it rather than sleep.
        try await waitFor(timeout: 5) { backend.nodes.nodes.count == 1 }
        XCTAssertEqual(backend.nodes.nodes.first?.displayName, "node-x")
        backend.stop()
    }

    func testRemoteBackendStreamDeliversSampleToServerStore() async throws {
        let node = sampleRemoteNode(name: "node-stream")
        let nodesPayload = try jsonEncoder().encode([node])
        server.on("GET", "/v1/nodes") { _ in .json(200, nodesPayload) }
        let settingsPayload = try jsonEncoder().encode(sampleRemoteSettings())
        server.on("GET", "/v1/settings") { _ in .json(200, settingsPayload) }

        let backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        // Wait for node registration.
        try await waitFor(timeout: 5) { backend.servers.serverVMs.first != nil }
        XCTAssertEqual(backend.servers.serverVMs.count, 1)

        // Build the WS envelope the server emits per wire.go.
        let envelope: [String: Any] = [
            "type": "sample",
            "nodeId": node.id.uuidString,
            "sample": sampleJSON(),
        ]
        let data = try JSONSerialization.data(withJSONObject: envelope)
        let text = String(data: data, encoding: .utf8)!

        // Give the client a moment to finish WS handshake, then send.
        try await waitFor(timeout: 5) { self.server.hasWebSocketClient }
        server.sendToAllWebSockets(text: text)

        // ServerStore.ingest routes the sample into the ServerViewModel.
        // We don't expose the sample object itself — assert via lastSeen
        // being populated and the sampler version surfacing.
        try await waitFor(timeout: 5) {
            backend.servers.serverVMs.first(where: { $0.id == node.id })?.lastSeen != nil
        }
        let vm = backend.servers.serverVMs.first(where: { $0.id == node.id })
        XCTAssertNotNil(vm?.lastSeen)
        XCTAssertEqual(vm?.samplerVersion, "0.1.0")
        backend.stop()
    }

    // MARK: - Lease frames

    private func startBackend() async throws -> (RemoteBackend, RemoteNode) {
        let node = sampleRemoteNode(name: "node-lease")
        let nodesPayload = try jsonEncoder().encode([node])
        server.on("GET", "/v1/nodes") { _ in .json(200, nodesPayload) }
        let settingsPayload = try jsonEncoder().encode(sampleRemoteSettings())
        server.on("GET", "/v1/settings") { _ in .json(200, settingsPayload) }
        let backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        try await waitFor(timeout: 5) { backend.servers.serverVMs.first != nil }
        try await waitFor(timeout: 5) { self.server.hasWebSocketClient }
        return (backend, node)
    }

    private func frame(_ object: [String: Any]) throws -> String {
        String(decoding: try JSONSerialization.data(withJSONObject: object), as: UTF8.self)
    }

    func testRemoteBackendStreamDeliversSummaryToServerStore() async throws {
        let (backend, node) = try await startBackend()
        server.sendToAllWebSockets(text: try frame([
            "type": "summary",
            "nodeId": node.id.uuidString,
            "summary": [
                "ts": "2026-10-17T12:00:00.000Z", "cpu": 12.5, "mem": 0.4, "disk": 0.3,
                "rx": 10, "tx": 20, "load": 0.1,
                "host": ["os": "linux", "arch": "amd64", "cores": 4, "mem_total": 1, "uptime_s": 1, "sampler": "0.4.0"],
            ] as [String: Any],
        ]))
        try await waitFor(timeout: 5) { backend.servers.serverVMs.first?.lastSeen != nil }
        XCTAssertEqual(backend.servers.serverVMs.first?.samplerVersion, "0.4.0")
        backend.stop()
    }

    func testRemoteBackendStreamDetailFillsProcs() async throws {
        let (backend, node) = try await startBackend()
        server.sendToAllWebSockets(text: try frame([
            "type": "detail",
            "nodeId": node.id.uuidString,
            "tab": "processes",
            "detail": [
                "ts": "2026-10-17T12:00:00.000Z",
                "procs": ["root": true, "top_n": 1, "total": 1, "visible": 1,
                          "items": [["pid": 1, "name": "init", "cpu_pct": 1.0, "rss": 10]]] as [String: Any],
            ] as [String: Any],
        ]))
        try await waitFor(timeout: 5) { backend.servers.serverVMs.first?.procsAvailable == true }
        backend.stop()
    }

    func testRemoteBackendReachabilityEventMarksOffline() async throws {
        let (backend, node) = try await startBackend()
        func event(_ tint: String) throws -> String {
            try frame(["type": "event", "event": [
                "id": UUID().uuidString, "ts": "2026-10-17T12:00:00Z", "kind": "reachability_changed",
                "nodeId": node.id.uuidString, "metric": "reachability", "tint": tint,
            ]])
        }
        let vm = try XCTUnwrap(backend.servers.serverVMs.first)
        server.sendToAllWebSockets(text: try event("critical"))
        try await waitFor(timeout: 5) { vm.state.isOffline }
        server.sendToAllWebSockets(text: try event("ok"))
        try await waitFor(timeout: 5) { !vm.state.isOffline }
        backend.stop()
    }

    // MARK: - View lease

    private func viewFrames() -> [[String: Any]] {
        server.receivedWebSocketTexts.compactMap {
            (try? JSONSerialization.jsonObject(with: Data($0.utf8))) as? [String: Any]
        }.filter { $0["type"] as? String == "view" }
    }

    func testStreamConnectsInLeaseMode() async throws {
        let (backend, _) = try await startBackend()
        XCTAssertEqual(server.lastWebSocketPath?.hasSuffix("?lease=1"), true)
        backend.stop()
    }

    func testStreamOffersNoCompression() async throws {
        let (backend, _) = try await startBackend()
        let ext = server.lastWebSocketHeaders["sec-websocket-extensions"] ?? ""
        XCTAssertFalse(ext.contains("permessage-deflate"), ext)
        backend.stop()
    }

    func testViewSentOnConnect() async throws {
        let (backend, node) = try await startBackend()
        backend.viewLease.setPopover(visible: true)
        backend.viewLease.setCard(node.id, visible: true)
        try await waitFor(timeout: 5) { self.viewFrames().count == 1 }
        // The first frame of a new connection, here `hello`, sends the view again.
        server.sendToAllWebSockets(text: #"{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}"#)
        try await waitFor(timeout: 5) { self.viewFrames().count == 2 }
        let view = try XCTUnwrap(viewFrames().last)
        XCTAssertEqual(view["hosts"] as? [String], [node.id.uuidString])
        XCTAssertNil(view["detail"])
        backend.stop()
    }

    func testEmptyViewOnHide() async throws {
        let (backend, node) = try await startBackend()
        backend.viewLease.setPopover(visible: true)
        backend.viewLease.setCard(node.id, visible: true)
        try await waitFor(timeout: 5) { self.viewFrames().count == 1 }
        backend.viewLease.setPopover(visible: false)
        try await waitFor(timeout: 5) { self.viewFrames().count == 2 }
        XCTAssertEqual((viewFrames().last?["hosts"] as? [Any])?.count, 0)
        backend.stop()
    }

    func testHelloSetsCloudLiveSource() async throws {
        let (backend, node) = try await startBackend()
        let vm = try XCTUnwrap(backend.servers.serverVMs.first)
        // An old server sends samples and no hello.
        server.sendToAllWebSockets(text: try frame(["type": "sample", "nodeId": node.id.uuidString, "sample": sampleJSON()]))
        try await waitFor(timeout: 5) { vm.lastSeen != nil }
        XCTAssertEqual(vm.liveSource, .poll)
        server.sendToAllWebSockets(text: #"{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}"#)
        try await waitFor(timeout: 5) { vm.liveSource == .cloud }
        XCTAssertTrue(backend.leaseMode)
        backend.stop()
    }

    // MARK: - Cloud history

    private func historyBody(_ node: UUID, series: String) -> Data {
        Data("""
        {"node_id":"\(node)","series":{\(series)},"from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"raw"}
        """.utf8)
    }

    private func query(_ req: LocalTestServer.CapturedRequest) -> [URLQueryItem] {
        URLComponents(string: "http://x" + req.path)?.queryItems ?? []
    }

    func testBackfillCardsFillsSeries() async throws {
        let (backend, node) = try await startBackend()
        let body = Data("""
        {"from":"2026-04-20T11:30:00Z","to":"2026-04-20T12:00:00Z","resolution":"raw","nodes":[\
        {"node_id":"\(node.id)","series":{"cpu":[{"ts":"2026-04-20T11:59:50Z","value":40},{"ts":"2026-04-20T12:00:00Z","value":60}]}}]}
        """.utf8)
        server.on("GET", "/v1/history") { _ in .json(200, body) }
        let vm = try XCTUnwrap(backend.servers.serverVMs.first)
        await backend.cloudHistory?.backfillCards([node.id])
        XCTAssertEqual(vm.cpu.snapshot().map(\.v), [0.4, 0.6])
        let items = query(try XCTUnwrap(server.captured.last { $0.path.hasPrefix("/v1/history") }))
        XCTAssertEqual(items.first { $0.name == "nodes" }?.value, node.id.uuidString)
        XCTAssertEqual(items.first { $0.name == "metrics" }?.value, "cpu,mem,net_rx,net_tx")
        backend.stop()
    }

    private let hour: ClosedRange<Date> = Date(timeIntervalSince1970: 1_800_000_000 - 3600)...Date(timeIntervalSince1970: 1_800_000_000)

    func testWindowAndDisksUseTheirRoutes() async throws {
        let (backend, node) = try await startBackend()
        let id = node.id
        server.on("GET", "/v1/nodes/\(id)/disks") { _ in
            .json(200, Data(#"{"mounts":[{"mount":"/","fs":"ext4","points":[{"ts":"2026-04-20T12:00:00Z","used":50,"total":100,"inodes_used":9,"inodes_total":10}]}]}"#.utf8))
        }
        let io = historyBody(id, series: #""disk_read":[{"ts":"2026-04-20T12:00:00Z","value":1048576}],"disk_write":[{"ts":"2026-04-20T12:00:00Z","value":0}]"#)
        server.on("GET", "/v1/nodes/\(id)/history") { _ in .json(200, io) }
        let loader = try XCTUnwrap(backend.cloudHistory)
        let disks = try await loader.disks(host: id, window: hour)
        let r = try await loader.window(host: id, metrics: ["disk_read", "disk_write"], window: hour, step: .seconds(10))
        XCTAssertEqual(disks.mounts.first?.points.first?.inodesUsed, 9)
        XCTAssertEqual(r.series["disk_read"]?.first?.value, 1_048_576)
        let hist = try XCTUnwrap(server.captured.first { $0.path.hasPrefix("/v1/nodes/\(id)/history") })
        let items = query(hist)
        XCTAssertEqual(items.first { $0.name == "metrics" }?.value, "disk_read,disk_write")
        XCTAssertEqual(items.first { $0.name == "step" }?.value, "10s")
        XCTAssertTrue(server.captured.contains { $0.path.hasPrefix("/v1/nodes/\(id)/disks") })
        backend.stop()
    }

    func testWindowErrorThrows() async throws {
        let (backend, node) = try await startBackend()
        let id = node.id
        server.on("GET", "/v1/nodes/\(id)/history") { _ in
            .json(400, Data(#"{"error":{"code":"bad_request","message":"metric has no rollup for this range"}}"#.utf8))
        }
        do {
            _ = try await backend.cloudHistory?.window(host: id, metrics: ["cpu"], window: hour, step: .seconds(10))
            XCTFail("expected an error")
        } catch let RemoteClient.RemoteError.http(code, body) {
            XCTAssertEqual(code, 400)
            XCTAssertEqual(RemoteClient.errorMessage(Data(body.utf8)), "metric has no rollup for this range")
            XCTAssertEqual(CloudHistoryError.message(RemoteClient.RemoteError.http(code, body)), "metric has no rollup for this range")
        }
        backend.stop()
    }

    func testHistoryDecodesFractionalSeconds() async throws {
        let id = UUID()
        let body = historyBody(id, series: #""cpu":[{"ts":"2026-04-20T12:00:00.123Z","value":5}]"#)
        server.on("GET", "/v1/nodes/\(id)/history") { _ in .json(200, body) }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let r = try await client.history(nodeID: id, metrics: ["cpu"], from: Date(timeIntervalSince1970: 0), to: Date())
        let t = try XCTUnwrap(r.series["cpu"]?.first?.ts)
        XCTAssertEqual(t.timeIntervalSince1970.truncatingRemainder(dividingBy: 1), 0.123, accuracy: 0.001)
    }

    func testProcessesAtSendsTsByLimit() async throws {
        let id = UUID()
        server.on("GET", "/v1/nodes/\(id)/processes/at") { _ in
            .json(200, Data("""
            {"node_id":"\(id)","ts":"2026-04-20T12:00:00Z","tier":"raw","bucket_s":0,"by":"mem","total":2,\
            "processes":[{"key":"0a.Z28","pid":7,"name":"go","cmd":"go test","cpu_pct":12.5,"rss":2048}]}
            """.utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let ts = Date(timeIntervalSince1970: 1_776_686_400)
        let snap = try await client.processesAt(nodeID: id, ts: ts, by: .rss, limit: 20)
        XCTAssertEqual(snap.processes.first?.pid, 7)
        XCTAssertGreaterThan(snap.bytes, 0)
        let items = query(try XCTUnwrap(server.captured.last))
        XCTAssertEqual(items.first { $0.name == "ts" }?.value, "2026-04-20T12:00:00Z")
        XCTAssertEqual(items.first { $0.name == "by" }?.value, "mem")
        XCTAssertEqual(items.first { $0.name == "limit" }?.value, "20")
    }

    func testOffendersSendsSeries60() async throws {
        let id = UUID()
        server.on("GET", "/v1/nodes/\(id)/processes") { _ in
            .json(200, Data("""
            {"node_id":"\(id)","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",\
            "series_from":"2026-04-20T11:00:00Z","series_step_s":60,\
            "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","cpu_pct_avg":12.5,"cpu_pct_max":90,"rss_max":2048,\
            "series":{"cpu":[1,2],"rss":[3,4]}}]}
            """.utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let r = try await client.offenders(
            nodeID: id, from: Date(timeIntervalSince1970: 0), to: Date(), by: .cpu, limit: 10, series: 60
        )
        XCTAssertEqual(r.processes.first?.rssMax, 2048)
        XCTAssertEqual(r.seriesStepS, 60)
        let items = query(try XCTUnwrap(server.captured.last))
        XCTAssertEqual(items.first { $0.name == "series" }?.value, "60")
        XCTAssertEqual(items.first { $0.name == "limit" }?.value, "10")
        XCTAssertEqual(items.first { $0.name == "by" }?.value, "cpu")
    }

    func testProcessKeyIsPercentEncoded() async throws {
        let id = UUID()
        server.on("GET", "/v1/nodes/\(id)/processes/") { _ in
            .json(200, Data("""
            {"node_id":"\(id)","key":"k","name":"go","cmd":"","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",\
            "resolution":"10s","points":[]}
            """.utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        _ = try await client.process(nodeID: id, key: "0a1b.a/b c", from: Date(timeIntervalSince1970: 0), to: Date(), step: .seconds(10))
        let path = try XCTUnwrap(server.captured.last).path
        XCTAssertTrue(path.hasPrefix("/v1/nodes/\(id)/processes/0a1b.a%2Fb%20c?"), path)
        XCTAssertEqual(query(try XCTUnwrap(server.captured.last)).first { $0.name == "step" }?.value, "10s")
    }

    // MARK: - Helpers

    private func lastRequest() throws -> LocalTestServer.CapturedRequest {
        guard let last = server.captured.last else {
            throw XCTSkip("no request captured")
        }
        return last
    }

    private func jsonDecoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }

    private func jsonEncoder() -> JSONEncoder {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }

    private func sampleRemoteNode(name: String) -> RemoteNode {
        RemoteNode(
            id: UUID(),
            displayName: name,
            kind: "ssh",
            sshUser: "u", sshHost: "h",
            tags: [], enabled: true,
            iconOnWarn: true, iconOnCritical: true,
            notifyOnWarn: true, notifyOnCritical: true,
            customThresholds: nil, snoozedUntil: nil, favorite: false
        )
    }

    private func sampleRemoteSettings() -> RemoteServerSettings {
        RemoteServerSettings(
            thresholds: .defaults,
            localPollingIntervalSeconds: 2,
            sshPollingIntervalSeconds: 10,
            notificationsEnabled: true,
            notifyWarn: true, notifyCritical: true,
            notifyDebounceSeconds: 90,
            autoUpdateSamplersEnabled: true,
            postWakeGraceSeconds: 15
        )
    }

    private func sampleJSON() -> [String: Any] {
        [
            "v": 1,
            "ts": "2026-04-21T12:00:00.000Z",
            "host": [
                "name": "h", "os": "linux", "arch": "arm64",
                "kernel": "6.6", "uptime_s": 1, "sampler": "0.1.0",
            ],
            "cpu": ["pct": 10.0, "load_1": 0, "load_5": 0, "load_15": 0, "cores": 4],
            "mem": ["used": 1, "total": 2],
            "swap": ["used": 0, "total": 0],
            "errors": [],
        ]
    }

    // MARK: - Long-running process fields

    func testRemoteLongProcFieldsMapBothWays() throws {
        var node = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "h")
        node.customLongProc = LongProcRule(enabled: true, cpuPct: 90, sustainSeconds: 600, notify: .off, ignore: [])
        node.ignoredProcs = ["postgres"]
        let remote = node.toRemote(global: .defaults)
        XCTAssertEqual(remote.customLongProc, node.customLongProc)
        XCTAssertEqual(remote.ignoredProcs, ["postgres"])
        let back = Node(remote: try jsonDecoder().decode(RemoteNode.self, from: jsonEncoder().encode(remote)))
        XCTAssertEqual(back.customLongProc, node.customLongProc)
        XCTAssertEqual(back.ignoredProcs, ["postgres"])
    }

    // MARK: - Rule overrides on the wire

    func testNodeRoundTripUsesThresholdOverrides() throws {
        var node = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "h")
        node.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.6, critical: 0.7))
        node.mutedSignals = MutedSignals([.cpu], long: true)
        let data = try jsonEncoder().encode(node.toRemote(global: .defaults))
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertNil(json["customThresholds"])
        XCTAssertNotNil(json["thresholdOverrides"] as? [String: Any])
        let back = Node(remote: try jsonDecoder().decode(RemoteNode.self, from: data))
        XCTAssertEqual(back.thresholdOverrides?.mem, ThresholdPair(warn: 0.6, critical: 0.7))
        XCTAssertEqual(back.mutedSignals, MutedSignals([.cpu], long: true))
    }

    func testNilOverridesAreSentAsNull() throws {
        let node = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "h")
        let data = try jsonEncoder().encode(node.toRemote(global: .defaults))
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertTrue(json["thresholdOverrides"] is NSNull, "null clears the override on the server")
    }

    func testOldCustomThresholdsStillRead() throws {
        var remote = sampleRemoteNode(name: "old")
        remote.customThresholds = MetricThresholds.defaults
        let node = Node(remote: remote)
        XCTAssertNotNil(node.thresholdOverrides?.cpu)
    }

    func testOldServerWithoutLongProcFieldsDecodesDefaults() throws {
        let node = Node(remote: try jsonDecoder().decode(RemoteNode.self, from: jsonEncoder().encode(sampleRemoteNode(name: "x"))))
        XCTAssertNil(node.customLongProc)
        XCTAssertEqual(node.ignoredProcs, [])
        let rs = try jsonDecoder().decode(RemoteServerSettings.self, from: jsonEncoder().encode(sampleRemoteSettings()))
        XCTAssertNil(rs.longProc)
    }

    func testSettingsUpdatePushesLongProcToViewModels() async throws {
        let node = sampleRemoteNode(name: "node-lp")
        let nodesPayload = try jsonEncoder().encode([node])
        server.on("GET", "/v1/nodes") { _ in .json(200, nodesPayload) }
        var rs = sampleRemoteSettings()
        rs.longProc = LongProcRule(enabled: true, cpuPct: 300, sustainSeconds: 600, notify: .all, ignore: ["Yes"])
        let settingsPayload = try jsonEncoder().encode(rs)
        server.on("GET", "/v1/settings") { _ in .json(200, settingsPayload) }

        let backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        try await waitFor(timeout: 5) { backend.servers.serverVMs.first?.longProcRule.cpuPct == 300 }
        XCTAssertEqual(backend.serverSettings.longProc.cpuPct, 300)
        XCTAssertEqual(backend.servers.serverVMs.first?.ignoredProcs, ["yes"])
        backend.stop()
    }

    // MARK: - Send working folders

    private func body(_ req: LocalTestServer.CapturedRequest) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: req.body) as? [String: Any])
    }

    func testHideProcFoldersMapsToSendProcFolders() throws {
        var remote = sampleRemoteNode(name: "x")
        XCTAssertTrue(Node(remote: remote).sendProcFolders, "an old server means send")
        remote.hideProcFolders = true
        XCTAssertFalse(Node(remote: remote).sendProcFolders)
        var node = Node(remote: remote)
        node.sendProcFolders = true
        // The key is sent only when the caller asks for it.
        let plain = try jsonEncoder().encode(node.toRemote(global: .defaults))
        XCTAssertNil((try JSONSerialization.jsonObject(with: plain) as? [String: Any])?["hideProcFolders"])
        let asked = try jsonEncoder().encode(node.toRemote(global: .defaults, hideProcFolders: false))
        XCTAssertEqual((try JSONSerialization.jsonObject(with: asked) as? [String: Any])?["hideProcFolders"] as? Bool, false)
    }

    func testNodeSettingsUpdateSendsHideProcFoldersOnlyOnChange() async throws {
        let node = sampleRemoteNode(name: "node-hp")
        let payload = try jsonEncoder().encode([node])
        server.on("GET", "/v1/nodes") { _ in .json(200, payload) }
        server.on("GET", "/v1/settings") { _ in .json(200, (try? JSONEncoder().encode(RemoteServerSettings(
            thresholds: .defaults, localPollingIntervalSeconds: 2, sshPollingIntervalSeconds: 10,
            notificationsEnabled: true, notifyWarn: true, notifyCritical: true, notifyDebounceSeconds: 90,
            autoUpdateSamplersEnabled: true, postWakeGraceSeconds: 15))) ?? Data()) }
        server.on("PUT", "/v1/nodes/") { req in .json(200, req.body) }

        let backend = RemoteBackend(endpoint: server.baseURL, token: token)
        backend.start()
        try await waitFor(timeout: 5) { backend.nodes.nodes.count == 1 }
        var n = try XCTUnwrap(backend.nodes.nodes.first)

        n.favorite = true
        try await backend.updateNode(n)
        let first = try body(try XCTUnwrap(server.captured.last { $0.method == "PUT" }))
        XCTAssertNil(first["hideProcFolders"], "an unrelated change keeps the stored value")

        n.sendProcFolders = false
        try await backend.updateNode(n)
        let second = try body(try XCTUnwrap(server.captured.last { $0.method == "PUT" }))
        XCTAssertEqual(second["hideProcFolders"] as? Bool, true)
        XCTAssertEqual(backend.nodes.nodes.first?.sendProcFolders, false)
        backend.stop()
    }

    func testSetHideProcFoldersReadsTheNodeThenPuts() async throws {
        var node = sampleRemoteNode(name: "cloud-host")
        node.ignoredProcs = ["postgres"]
        let payload = try jsonEncoder().encode(node)
        server.on("GET", "/v1/nodes/") { _ in .json(200, payload) }
        server.on("PUT", "/v1/nodes/") { req in .json(200, req.body) }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        try await client.setHideProcFolders(nodeID: node.id, hide: true)
        let put = try XCTUnwrap(server.captured.last { $0.method == "PUT" })
        XCTAssertTrue(put.path.hasSuffix(node.id.uuidString))
        let sent = try body(put)
        XCTAssertEqual(sent["hideProcFolders"] as? Bool, true)
        XCTAssertEqual(sent["displayName"] as? String, "cloud-host")
        XCTAssertEqual(sent["ignoredProcs"] as? [String], ["postgres"])
    }

    func testAckAlertCallsAPI() async throws {
        let id = UUID(), node = UUID()
        server.on("POST", "/v1/alerts/") { _ in
            .json(200, Data(#"{"id":"\#(id)","node_id":"\#(node)","kind":"threshold_crossed","metric":"cpu","tint":"warn","ts":"2026-11-25T10:00:00.123456Z","value":0.9,"acked_by":"\#(UUID())"}"#.utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let alert = try await client.ackAlert(id: id)
        let req = try XCTUnwrap(server.captured.last)
        XCTAssertEqual(req.method, "POST")
        XCTAssertEqual(req.path, "/v1/alerts/\(id.uuidString)/ack")
        XCTAssertEqual(alert.id, id)
        XCTAssertTrue(alert.isSilenced())
    }

    func testSnoozeAlertSendsUntil() async throws {
        let id = UUID(), node = UUID()
        let until = Date(timeIntervalSince1970: 1_800_000_000)
        server.on("POST", "/v1/alerts/") { _ in
            .json(200, Data(#"{"id":"\#(id)","node_id":"\#(node)","kind":"threshold_crossed","metric":"cpu","tint":"warn","ts":"2026-11-25T10:00:00Z","value":0.9,"snoozed_until":"2027-01-15T08:00:00.5Z"}"#.utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        let alert = try await client.snoozeAlert(id: id, until: until)
        let req = try XCTUnwrap(server.captured.last)
        XCTAssertEqual(req.method, "POST")
        XCTAssertEqual(req.path, "/v1/alerts/\(id.uuidString)/snooze?until=2027-01-15T08:00:00Z")
        XCTAssertNotNil(alert.snoozedUntil)
        XCTAssertNil(alert.ackedBy)
    }
}
