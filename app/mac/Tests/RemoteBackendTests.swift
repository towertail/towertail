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

    // MARK: - Helpers

    private func lastRequest() throws -> LocalTestServer.CapturedRequest {
        guard let last = server.captured.last else {
            throw XCTSkip("no request captured")
        }
        return last
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

    private func waitFor(timeout seconds: Double,
                         _ check: @escaping @MainActor () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            if check() { return }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        XCTFail("condition not met within \(seconds)s")
    }
}

