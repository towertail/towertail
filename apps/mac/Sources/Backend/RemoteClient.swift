import Foundation

/// Minimal HTTP + WebSocket client for the Towertail server. Owned by
/// `RemoteBackend`. Not a generic SDK — it only covers the endpoints
/// the Mac client needs. All calls are async and throw; callers route
/// failures to the UI via `BackendError`.
@MainActor
final class RemoteClient {
    struct Config {
        var endpoint: URL
        var token: String
        var insecure: Bool = false
    }

    enum RemoteError: Error, LocalizedError {
        case http(Int, String)
        case decode(String)
        case transport(Error)

        var errorDescription: String? {
            switch self {
            case .http(let code, let body): return "HTTP \(code): \(body)"
            case .decode(let msg): return "decode: \(msg)"
            case .transport(let err): return "transport: \(err.localizedDescription)"
            }
        }
    }

    var config: Config
    private let urlSession: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder

    init(config: Config) {
        self.config = config
        let sconfig = URLSessionConfiguration.default
        sconfig.waitsForConnectivity = true
        sconfig.timeoutIntervalForRequest = 30
        self.urlSession = URLSession(configuration: sconfig)
        self.decoder = JSONDecoder()
        self.decoder.dateDecodingStrategy = .iso8601
        self.encoder = JSONEncoder()
        self.encoder.dateEncodingStrategy = .iso8601
    }

    // MARK: - REST

    func listNodes() async throws -> [RemoteNode] {
        try await get("/v1/nodes")
    }

    func createNode(_ node: RemoteNode) async throws -> RemoteNode {
        try await send("POST", "/v1/nodes", body: node)
    }

    func updateNode(_ node: RemoteNode) async throws -> RemoteNode {
        try await send("PUT", "/v1/nodes/\(node.id.uuidString)", body: node)
    }

    func deleteNode(id: UUID) async throws {
        _ = try await raw("DELETE", "/v1/nodes/\(id.uuidString)")
    }

    func getSettings() async throws -> RemoteServerSettings {
        try await get("/v1/settings")
    }

    func putSettings(_ s: RemoteServerSettings) async throws -> RemoteServerSettings {
        try await send("PUT", "/v1/settings", body: s)
    }

    func killProcess(nodeID: UUID, pid: Int32) async throws {
        _ = try await raw("POST", "/v1/nodes/\(nodeID.uuidString)/kill-process?pid=\(pid)")
    }

    // MARK: - Disk scans (docs/wire.md §3.1)

    /// Starts a scan job. 403 gives `scansOff`, 409 gives `alreadyRunning`
    /// with the job that runs.
    func startDiskScan(nodeId: UUID, request: DiskScanRequest) async throws -> DiskScanJob {
        let (data, http) = try await response("POST", "/v1/nodes/\(nodeId.uuidString)/disk-scans",
                                              body: try encoder.encode(request))
        switch http.statusCode {
        case 200..<300:
            return try decode(DiskScanJob.self, data)
        case 403:
            let msg = Self.errorMessage(data) ?? "disk scans are off for this node"
            throw DiskScanError.scansOff(Self.sentence(msg))
        case 409:
            struct Conflict: Decodable { let job: DiskScanJob? }
            throw DiskScanError.alreadyRunning(try? decoder.decode(Conflict.self, from: data).job)
        default:
            throw RemoteError.http(http.statusCode, String(decoding: data, as: UTF8.self))
        }
    }

    /// Jobs of a node, newest first.
    func diskScans(nodeId: UUID) async throws -> [DiskScanJob] {
        struct List: Decodable { let jobs: [DiskScanJob] }
        let list: List = try await get("/v1/nodes/\(nodeId.uuidString)/disk-scans")
        return list.jobs
    }

    func diskScan(jobId: String) async throws -> DiskScanJob {
        try await get("/v1/disk-scans/\(jobId)")
    }

    func cancelDiskScan(jobId: String) async throws -> DiskScanJob {
        try decode(DiskScanJob.self, try await raw("POST", "/v1/disk-scans/\(jobId)/cancel"))
    }

    /// The result of a done job, or nil when the server has none (404).
    func diskScanResult(jobId: String) async throws -> DiskScanResult? {
        let (data, http) = try await response("GET", "/v1/disk-scans/\(jobId)/result")
        if http.statusCode == 404 { return nil }
        guard http.statusCode < 400 else {
            throw RemoteError.http(http.statusCode, String(decoding: data, as: UTF8.self))
        }
        // URLSession decodes gzip when it sees Content-Encoding. Decode here
        // when a proxy removed the header.
        var body = data
        if GzipDecoder.isGzip(body) {
            guard let plain = GzipDecoder.decode(body) else { throw RemoteError.decode("bad gzip result") }
            body = plain
        }
        guard case .result(let r) = try DiskScanEvent.decodeLine(body) else {
            throw RemoteError.decode("the result is not a scan result line")
        }
        return r
    }

    /// The message of a wire.md error envelope.
    static func errorMessage(_ data: Data) -> String? {
        struct Envelope: Decodable {
            struct Err: Decodable { let message: String? }
            let error: Err
        }
        return (try? JSONDecoder().decode(Envelope.self, from: data))?.error.message
    }

    /// "disk scans are off" → "Disk scans are off."
    private static func sentence(_ s: String) -> String {
        guard let first = s.first else { return s }
        let t = first.uppercased() + s.dropFirst()
        return t.hasSuffix(".") ? t : t + "."
    }

    // MARK: - Transport helpers

    private func decode<T: Decodable>(_ type: T.Type, _ data: Data) throws -> T {
        do { return try decoder.decode(T.self, from: data) }
        catch { throw RemoteError.decode(String(describing: error)) }
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        let data = try await raw("GET", path)
        do { return try decoder.decode(T.self, from: data) }
        catch { throw RemoteError.decode(String(describing: error)) }
    }

    private func send<T: Decodable, B: Encodable>(_ method: String, _ path: String, body: B) async throws -> T {
        let data = try await raw(method, path, body: try encoder.encode(body))
        do { return try decoder.decode(T.self, from: data) }
        catch { throw RemoteError.decode(String(describing: error)) }
    }

    private func raw(_ method: String, _ path: String, body: Data? = nil) async throws -> Data {
        let (data, http) = try await response(method, path, body: body)
        if http.statusCode >= 400 {
            throw RemoteError.http(http.statusCode, String(data: data, encoding: .utf8) ?? "")
        }
        return data
    }

    /// Sends a request and returns the response for any status code.
    private func response(_ method: String, _ path: String, body: Data? = nil) async throws -> (Data, HTTPURLResponse) {
        var req = URLRequest(url: config.endpoint.appendingPathComponent(path))
        // URLComponents for ?query handling in callers that already
        // embedded them.
        if path.contains("?") {
            req = URLRequest(url: URL(string: config.endpoint.absoluteString + path)!)
        }
        req.httpMethod = method
        req.addValue("Bearer \(config.token)", forHTTPHeaderField: "Authorization")
        if body != nil {
            req.addValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        do {
            let (data, resp) = try await urlSession.data(for: req)
            guard let http = resp as? HTTPURLResponse else {
                throw RemoteError.transport(URLError(.badServerResponse))
            }
            return (data, http)
        } catch let err as RemoteError {
            throw err
        } catch {
            throw RemoteError.transport(error)
        }
    }

    // MARK: - WebSocket

    /// Opens a WS connection and invokes `onMessage` for each received
    /// text frame. `onConnect` runs at the first frame of each connection,
    /// so callers can refetch state that they missed. The returned task
    /// runs until cancelled.
    func openStream(onConnect: (@Sendable () -> Void)? = nil,
                    onMessage: @escaping @Sendable (String) -> Void) -> Task<Void, Never> {
        return Task.detached { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                do {
                    try await self.streamOnce(onConnect: onConnect, onMessage: onMessage)
                } catch {
                    // transient; backoff and retry.
                }
                let jitter = UInt64.random(in: 500...1500) * 1_000_000
                try? await Task.sleep(nanoseconds: jitter)
            }
        }
    }

    private func streamOnce(onConnect: (@Sendable () -> Void)?,
                            onMessage: @escaping @Sendable (String) -> Void) async throws {
        var components = URLComponents(url: config.endpoint.appendingPathComponent("/v1/stream"), resolvingAgainstBaseURL: false)!
        if components.scheme == "https" {
            components.scheme = "wss"
        } else if components.scheme == "http" {
            components.scheme = "ws"
        }
        var req = URLRequest(url: components.url!)
        req.addValue("Bearer \(config.token)", forHTTPHeaderField: "Authorization")
        let task = urlSession.webSocketTask(with: req)
        task.resume()
        defer { task.cancel(with: .normalClosure, reason: nil) }

        var first = true
        while !Task.isCancelled {
            let message = try await task.receive()
            if first {
                first = false
                onConnect?()
            }
            switch message {
            case .string(let s):
                onMessage(s)
            case .data(let d):
                if let s = String(data: d, encoding: .utf8) { onMessage(s) }
            @unknown default:
                break
            }
        }
    }
}

// MARK: - Wire DTOs

struct RemoteNode: Codable, Sendable, Identifiable {
    var id: UUID
    var displayName: String
    var kind: String
    var sshUser: String?
    var sshHost: String?
    var tags: [String]
    var enabled: Bool
    var iconOnWarn: Bool
    var iconOnCritical: Bool
    var notifyOnWarn: Bool
    var notifyOnCritical: Bool
    var customThresholds: MetricThresholds?
    var snoozedUntil: Date?
    var favorite: Bool
    /// Nil when the server is older than disk scans.
    var allowDiskScan: Bool?
}

struct RemoteServerSettings: Codable, Sendable {
    var thresholds: MetricThresholds
    var localPollingIntervalSeconds: Int
    var sshPollingIntervalSeconds: Int
    var notificationsEnabled: Bool
    var notifyWarn: Bool
    var notifyCritical: Bool
    var notifyDebounceSeconds: Int
    var autoUpdateSamplersEnabled: Bool
    var postWakeGraceSeconds: Int
}

extension Node {
    init(remote r: RemoteNode) {
        self.init(
            id: r.id,
            displayName: r.displayName,
            kind: NodeKind(rawValue: r.kind) ?? .ssh,
            sshUser: r.sshUser,
            sshHost: r.sshHost,
            tags: r.tags,
            enabled: r.enabled,
            iconOnWarn: r.iconOnWarn,
            iconOnCritical: r.iconOnCritical,
            notifyOnWarn: r.notifyOnWarn,
            notifyOnCritical: r.notifyOnCritical,
            thresholdOverrides: r.customThresholds.map(ThresholdOverrides.init(legacy:)),
            snoozedUntil: r.snoozedUntil,
            favorite: r.favorite,
            allowDiskScan: r.allowDiskScan ?? false
        )
    }

    /// The server takes a full threshold set, so unset metrics are filled
    /// from `global`.
    func toRemote(global: MetricThresholds) -> RemoteNode {
        RemoteNode(
            id: id,
            displayName: displayName,
            kind: kind.rawValue,
            sshUser: sshUser,
            sshHost: sshHost,
            tags: tags,
            enabled: enabled,
            iconOnWarn: iconOnWarn,
            iconOnCritical: iconOnCritical,
            notifyOnWarn: notifyOnWarn,
            notifyOnCritical: notifyOnCritical,
            customThresholds: thresholdOverrides?.applied(to: global),
            snoozedUntil: snoozedUntil,
            favorite: favorite,
            allowDiskScan: allowDiskScan
        )
    }
}
