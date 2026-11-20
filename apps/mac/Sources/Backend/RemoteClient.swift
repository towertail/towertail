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

    /// The open alerts of the org. The app reads the reachability ones.
    func openAlerts() async throws -> [RemoteAlert] {
        struct List: Decodable { let alerts: [RemoteAlert] }
        let list: List = try await get("/v1/alerts")
        return list.alerts
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
    /// text frame. `lease` selects lease mode (`?lease=1`, docs/wire.md §4).
    /// `onConnect` runs once for each connection, at the first
    /// frame or at the first pong, so callers can refetch state that they
    /// missed. `onDisconnect` runs each time a connection attempt
    /// ends. The loop runs until `cancel()` of the returned stream.
    func openStream(lease: Bool = false,
                    onConnect: (@Sendable () -> Void)? = nil,
                    onDisconnect: (@Sendable () -> Void)? = nil,
                    onMessage: @escaping @Sendable (String) -> Void) -> RemoteStream {
        let stream = RemoteStream()
        stream.start(Task.detached { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                do {
                    try await self.streamOnce(stream: stream, lease: lease, onConnect: onConnect, onMessage: onMessage)
                } catch {
                    // transient; backoff and retry.
                }
                onDisconnect?()
                let jitter = UInt64.random(in: 500...1500) * 1_000_000
                try? await Task.sleep(nanoseconds: jitter)
            }
        })
        return stream
    }

    private func streamOnce(stream: RemoteStream, lease: Bool,
                            onConnect: (@Sendable () -> Void)?,
                            onMessage: @escaping @Sendable (String) -> Void) async throws {
        var components = URLComponents(url: config.endpoint.appendingPathComponent("/v1/stream"), resolvingAgainstBaseURL: false)!
        if components.scheme == "https" {
            components.scheme = "wss"
        } else if components.scheme == "http" {
            components.scheme = "ws"
        }
        if lease { components.queryItems = [URLQueryItem(name: "lease", value: "1")] }
        var req = URLRequest(url: components.url!)
        req.addValue("Bearer \(config.token)", forHTTPHeaderField: "Authorization")
        let task = urlSession.webSocketTask(with: req)
        task.resume()
        stream.attach(task)
        defer {
            stream.detach(task)
            task.cancel(with: .normalClosure, reason: nil)
        }

        // A server with no data to replay sends no first frame, so a ping
        // also tells that the connection is up.
        let connect = OnceGate(onConnect)
        task.sendPing { error in
            if error == nil { connect.fire() }
        }
        while !Task.isCancelled {
            let message = try await task.receive()
            connect.fire()
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

/// A stream that `RemoteClient.openStream` opened. It sends frames on the
/// open connection and stops the reconnect loop.
final class RemoteStream: @unchecked Sendable {
    private let lock = NSLock()
    private var loop: Task<Void, Never>?
    private var socket: URLSessionWebSocketTask?

    fileprivate func start(_ loop: Task<Void, Never>) {
        lock.withLock { self.loop = loop }
    }

    fileprivate func attach(_ task: URLSessionWebSocketTask) {
        lock.withLock { socket = task }
    }

    fileprivate func detach(_ task: URLSessionWebSocketTask) {
        lock.withLock { if socket === task { socket = nil } }
    }

    /// Sends one `view` frame. Without a connection it drops the frame:
    /// the caller sends the view again when the stream connects.
    func send(_ view: LiveView) async {
        guard let task = lock.withLock({ socket }),
              let data = try? JSONEncoder().encode(view) else { return }
        do {
            try await task.send(.string(String(decoding: data, as: UTF8.self)))
        } catch {
            await Logger.shared.debug("stream: view send failed: \(error)", category: "cloud")
        }
    }

    func cancel() {
        let (loop, socket) = lock.withLock { (self.loop, self.socket) }
        loop?.cancel()
        socket?.cancel(with: .normalClosure, reason: nil)
    }
}

/// Runs a closure at most one time, from any thread.
private final class OnceGate: @unchecked Sendable {
    private let lock = NSLock()
    private var action: (@Sendable () -> Void)?

    init(_ action: (@Sendable () -> Void)?) { self.action = action }

    func fire() {
        lock.lock()
        let run = action
        action = nil
        lock.unlock()
        run?()
    }
}

// MARK: - Wire DTOs

struct RemoteAlert: Decodable, Sendable {
    let nodeId: UUID
    let metric: String
    let tint: String

    enum CodingKeys: String, CodingKey {
        case nodeId = "node_id"
        case metric, tint
    }
}

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
    /// Nil when the server is older than long-running process alerts.
    var customLongProc: LongProcRule?
    var ignoredProcs: [String]?
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
    /// Nil when the server is older than long-running process alerts.
    var longProc: LongProcRule?
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
            customLongProc: r.customLongProc,
            ignoredProcs: r.ignoredProcs ?? [],
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
            allowDiskScan: allowDiskScan,
            customLongProc: customLongProc,
            ignoredProcs: ignoredProcs
        )
    }
}

extension RemoteClient: Enroller {
    func enroll(nodeID: UUID, displayName: String, machineID: String?) async throws -> Enrollment {
        struct Body: Encodable {
            let displayName: String
            let nodeId: UUID
            let machineId: String?

            enum CodingKeys: String, CodingKey {
                case displayName = "display_name"
                case nodeId = "node_id"
                case machineId = "machine_id"
            }
        }
        return try await send(
            "POST", "/v1/sampler/enroll",
            body: Body(displayName: displayName, nodeId: nodeID, machineId: machineID)
        )
    }
}
