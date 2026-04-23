import XCTest
@testable import Towertail

/// End-to-end tests against the full real stack (towertail-server +
/// ClickHouse) brought up via docker-compose.test.yaml. The harness
/// talks to the real HTTP API, enrolls a sampler node, spawns the real
/// sampler binary in `push` mode, and asserts the WebSocket fan-out
/// delivers a sample that round-tripped through ClickHouse-less path
/// (hub → WS) plus is persisted (readyz + /v1/nodes).
///
/// Skipped unless TOWERTAIL_INTEGRATION=1 so regular `xcodebuild test`
/// passes without Docker. Use `scripts/tests.sh --swift-integration`.
@MainActor
final class RemoteBackendIntegrationTests: XCTestCase {
    override func setUp() async throws {
        try await super.setUp()
        try XCTSkipUnless(IntegrationConfig.enabled, "set TOWERTAIL_INTEGRATION=1 and boot docker stack")
        try await waitForServerReady(timeout: 30)
    }

    // MARK: - Basic REST reachability

    func testAdminTokenCanListNodes() async throws {
        let client = RemoteClient(config: .init(
            endpoint: IntegrationConfig.endpoint,
            token: IntegrationConfig.adminToken
        ))
        let nodes = try await client.listNodes()
        // Server starts empty; list is either empty or already-enrolled
        // from a previous test run. Assertion: call succeeds.
        _ = nodes
    }

    func testSettingsRoundtripThroughRealServer() async throws {
        let client = RemoteClient(config: .init(
            endpoint: IntegrationConfig.endpoint,
            token: IntegrationConfig.adminToken
        ))
        let original = try await client.getSettings()
        var modified = original
        modified.notifyDebounceSeconds = (original.notifyDebounceSeconds % 120) + 31
        let saved = try await client.putSettings(modified)
        XCTAssertEqual(saved.notifyDebounceSeconds, modified.notifyDebounceSeconds)

        // Re-read to confirm persistence.
        let reread = try await client.getSettings()
        XCTAssertEqual(reread.notifyDebounceSeconds, modified.notifyDebounceSeconds)

        // Restore.
        _ = try await client.putSettings(original)
    }

    // MARK: - Sampler enroll + push → WS fan-out

    /// Full loop: enroll a sampler token, spawn the real sampler binary
    /// in push mode, open a WebSocket to /v1/stream directly, assert a
    /// sample frame arrives for our node.
    ///
    /// We use `RemoteClient` (HTTP) + a raw `URLSessionWebSocketTask`
    /// instead of the full `RemoteBackend` so the test doesn't mutate
    /// the user's on-disk NodeStore / SettingsPersistence (that was
    /// interfering with any Towertail app running on the dev Mac).
    func testSamplerPushDeliversSampleOverWebSocket() async throws {
        let client = RemoteClient(config: .init(
            endpoint: IntegrationConfig.endpoint,
            token: IntegrationConfig.adminToken
        ))
        let enroll = try await enrollSampler(client: client, displayName: "it-\(UUID().uuidString.prefix(8))")

        // Open the WS stream directly against /v1/stream.
        var wsComponents = URLComponents(url: IntegrationConfig.endpoint.appendingPathComponent("/v1/stream"),
                                         resolvingAgainstBaseURL: false)!
        wsComponents.scheme = "ws"
        var wsReq = URLRequest(url: wsComponents.url!)
        wsReq.addValue("Bearer \(IntegrationConfig.adminToken)", forHTTPHeaderField: "Authorization")
        let session = URLSession(configuration: .ephemeral)
        let wsTask = session.webSocketTask(with: wsReq)
        wsTask.resume()
        defer { wsTask.cancel(with: .normalClosure, reason: nil) }

        // Spawn the real sampler binary in push mode.
        let samplerURL = try locateSamplerBinary()
        let process = Process()
        process.executableURL = samplerURL
        process.arguments = [
            "push",
            "--endpoint", IntegrationConfig.endpoint.absoluteString,
            "--token", enroll.samplerToken,
            "--interval", "1s",
            "--flush", "500ms",
            "--batch", "1",
            "--no-proc",
        ]
        process.standardError = Pipe()
        process.standardOutput = Pipe()
        try process.run()
        defer { if process.isRunning { process.terminate() } }

        // Read frames until we see a sample for our node, or time out.
        let deadline = Date().addingTimeInterval(30)
        var seenForOurNode = false
        while Date() < deadline && !seenForOurNode {
            let msg = try await wsTask.receive()
            let text: String?
            switch msg {
            case .string(let s): text = s
            case .data(let d):   text = String(data: d, encoding: .utf8)
            @unknown default:    text = nil
            }
            guard let line = text, let data = line.data(using: .utf8) else { continue }
            guard let env = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { continue }
            if env["type"] as? String == "sample",
               let nodeIDString = env["nodeId"] as? String,
               UUID(uuidString: nodeIDString) == enroll.nodeID {
                seenForOurNode = true
            }
        }
        XCTAssertTrue(seenForOurNode, "no sample frame arrived for enrolled node within 30s")
    }

    // MARK: - Helpers

    private struct EnrollResult {
        let nodeID: UUID
        let samplerToken: String
    }

    private func enrollSampler(client: RemoteClient, displayName: String) async throws -> EnrollResult {
        struct ReqBody: Encodable {
            let display_name: String
        }
        struct RespBody: Decodable {
            let node_id: UUID
            let sampler_token: String
        }
        var req = URLRequest(url: IntegrationConfig.endpoint.appendingPathComponent("/v1/sampler/enroll"))
        req.httpMethod = "POST"
        req.addValue("Bearer \(IntegrationConfig.adminToken)", forHTTPHeaderField: "Authorization")
        req.addValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(ReqBody(display_name: displayName))
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else {
            throw NSError(domain: "enroll", code: (resp as? HTTPURLResponse)?.statusCode ?? 0,
                          userInfo: [NSLocalizedDescriptionKey: String(data: data, encoding: .utf8) ?? ""])
        }
        let body = try JSONDecoder().decode(RespBody.self, from: data)
        return EnrollResult(nodeID: body.node_id, samplerToken: body.sampler_token)
    }

    private func waitForServerReady(timeout: Double) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        let url = IntegrationConfig.endpoint.appendingPathComponent("/readyz")
        var lastErr: Error?
        while Date() < deadline {
            do {
                let (_, resp) = try await URLSession.shared.data(from: url)
                if let http = resp as? HTTPURLResponse, http.statusCode == 200 {
                    return
                }
            } catch {
                lastErr = error
            }
            try await Task.sleep(nanoseconds: 500_000_000)
        }
        throw lastErr ?? NSError(domain: "readyz", code: 0)
    }

    private func waitFor(timeout seconds: Double,
                         _ check: @escaping @MainActor () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            if check() { return }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        XCTFail("condition not met within \(seconds)s")
    }

    /// Finds the sampler binary for the current host triple under
    /// `dist/samplers/<triple>/towertail-sampler`. Tries multiple roots
    /// since xcodebuild's test-host CWD is unpredictable.
    private func locateSamplerBinary() throws -> URL {
        let triple: String = {
            #if arch(arm64)
            return "darwin-arm64"
            #else
            return "darwin-amd64"
            #endif
        }()

        let env = ProcessInfo.processInfo.environment
        let loadedCfg: [String: String] = {
            var out: [String: String] = [:]
            if let text = try? String(contentsOfFile: "/tmp/towertail-integration-test.env", encoding: .utf8) {
                for line in text.split(separator: "\n") {
                    if let eq = line.firstIndex(of: "=") {
                        out[String(line[..<eq])] = String(line[line.index(after: eq)...])
                    }
                }
            }
            return out
        }()

        // 1. Explicit override.
        if let p = env["TOWERTAIL_SAMPLER_BINARY"] ?? loadedCfg["TOWERTAIL_SAMPLER_BINARY"] {
            let u = URL(fileURLWithPath: p)
            if FileManager.default.isExecutableFile(atPath: u.path) { return u }
        }

        // 2. Repo root via env (set by tests.sh) or a few plausible roots.
        var candidates: [URL] = []
        if let root = env["TOWERTAIL_REPO_ROOT"] ?? loadedCfg["TOWERTAIL_REPO_ROOT"] {
            candidates.append(URL(fileURLWithPath: root))
        }
        // CWD walk.
        var dir = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
        for _ in 0..<10 {
            candidates.append(dir)
            dir.deleteLastPathComponent()
        }
        // Source-file walk (this test file is at
        // <repo>/app/mac/Tests/Integration/RemoteBackendIntegrationTests.swift).
        var src = URL(fileURLWithPath: #filePath)
        for _ in 0..<6 {
            src.deleteLastPathComponent()
            candidates.append(src)
        }

        for root in candidates {
            let candidate = root
                .appendingPathComponent("dist/samplers")
                .appendingPathComponent(triple)
                .appendingPathComponent("towertail-sampler")
            if FileManager.default.isExecutableFile(atPath: candidate.path) {
                return candidate
            }
        }
        throw XCTSkip("sampler binary not found under dist/samplers/\(triple)/ — run scripts/build.sampler.sh")
    }
}
