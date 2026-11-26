import Foundation

/// Minimal HTTP + WebSocket client for the Towertail server. Owned by
/// `RemoteBackend`. Not a generic SDK — it only covers the endpoints
/// the Mac client needs. All calls are async and throw; callers route
/// failures to the UI.
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
        /// The role of the user is too low. The text is for the user.
        case forbidden(String)

        var errorDescription: String? {
            switch self {
            case .forbidden(let msg): return msg
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

    func getNode(id: UUID) async throws -> RemoteNode {
        try await get("/v1/nodes/\(id.uuidString)")
    }

    /// Sets "Send working folders" on the server node. The sampler of a push
    /// host gets the value in the reply of its next push. It reads the node
    /// first because `PUT` replaces the other fields.
    func setHideProcFolders(nodeID: UUID, hide: Bool) async throws {
        var node = try await getNode(id: nodeID)
        node.hideProcFolders = hide
        node.rulesOrigin = nil
        _ = try await updateNode(node)
    }

    // MARK: - Rules (docs/wire.md §3, `HostRules`, `RulesView`)

    func rules() async throws -> RemoteRulesView {
        try await get("/v1/rules", decoder: SampleCodec.decoder())
    }

    /// Replaces all rule fields of the host. The reply is the node, which
    /// the caller does not need: `rules()` gives the effective rules.
    func putHostRules(nodeID: UUID, _ rules: RemoteHostRules) async throws {
        _ = try await rulesCall("PUT", "/v1/nodes/\(nodeID.uuidString)/rules", body: try encoder.encode(rules))
    }

    func setMute(_ metric: String, nodeID: UUID?, muted: Bool) async throws {
        struct Body: Encodable { let metric: String; let nodeId: UUID?; let muted: Bool }
        _ = try await rulesCall("PUT", "/v1/mutes", body: try encoder.encode(Body(metric: metric, nodeId: nodeID, muted: muted)))
    }

    func importRules(_ body: RemoteRulesImport) async throws -> RemoteRulesImportResult {
        let data = try await rulesCall("POST", "/v1/rules/import", body: try encoder.encode(body))
        return try decode(RemoteRulesImportResult.self, data)
    }

    /// The backups of the last 30 days, newest first. Needs the admin role.
    func rulesBackups() async throws -> [RemoteRulesBackup] {
        try await get("/v1/rules/backups", decoder: SampleCodec.decoder())
    }

    func restoreRules(backupID: UUID, device: String) async throws -> RemoteRulesImportResult {
        struct Body: Encodable { let client = "mac"; let device: String }
        let data = try await rulesCall("POST", "/v1/rules/backups/\(backupID.uuidString)/restore", body: try encoder.encode(Body(device: device)))
        return try decode(RemoteRulesImportResult.self, data)
    }

    /// A 403 gives a plain message: the role of the user is too low.
    private func rulesCall(_ method: String, _ path: String, body: Data) async throws -> Data {
        let (data, http) = try await response(method, path, body: body)
        if http.statusCode == 403 { throw RemoteError.forbidden("Only an org admin can change this.") }
        if http.statusCode >= 400 {
            throw RemoteError.http(http.statusCode, Self.errorMessage(data) ?? String(decoding: data, as: UTF8.self))
        }
        return data
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

    func ackAlert(id: UUID) async throws -> RemoteAlert {
        try await send("POST", "/v1/alerts/\(id.uuidString)/ack", body: EmptyBody())
    }

    func snoozeAlert(id: UUID, until: Date) async throws -> RemoteAlert {
        let stamp = ISO8601DateFormatter().string(from: until)
        return try await send("POST", "/v1/alerts/\(id.uuidString)/snooze?until=\(stamp)", body: EmptyBody())
    }

    // MARK: - History (docs/wire.md §3.1)

    func history(nodeID: UUID, metrics: [String], from: Date, to: Date, step: Duration? = nil) async throws -> RemoteHistoryResponse {
        var query = [URLQueryItem(name: "metrics", value: metrics.joined(separator: ","))]
        query += Self.rangeItems(from: from, to: to)
        if let step { query.append(URLQueryItem(name: "step", value: "\(step.components.seconds)s")) }
        return try await get("/v1/nodes/\(nodeID.uuidString)/history", query: query, decoder: SampleCodec.decoder())
    }

    /// The server takes 200 nodes at most for each request.
    func fleetHistory(nodeIDs: [UUID], metrics: [String], from: Date, to: Date) async throws -> RemoteFleetHistory {
        var merged: RemoteFleetHistory?
        for start in stride(from: 0, to: nodeIDs.count, by: Self.fleetGroupSize) {
            let group = nodeIDs[start..<min(start + Self.fleetGroupSize, nodeIDs.count)]
            let query = [
                URLQueryItem(name: "nodes", value: group.map(\.uuidString).joined(separator: ",")),
                URLQueryItem(name: "metrics", value: metrics.joined(separator: ",")),
            ] + Self.rangeItems(from: from, to: to)
            let part: RemoteFleetHistory = try await get("/v1/history", query: query, decoder: SampleCodec.decoder())
            merged = merged.map {
                RemoteFleetHistory(
                    from: $0.from, to: $0.to, resolution: $0.resolution,
                    clamped: ($0.clamped ?? false) || (part.clamped ?? false) ? true : nil,
                    nodes: $0.nodes + part.nodes
                )
            } ?? part
        }
        return merged ?? RemoteFleetHistory(from: from, to: to, resolution: "raw", clamped: nil, nodes: [])
    }

    func processesAt(nodeID: UUID, ts: Date, by: ProcSort, limit: Int) async throws -> RemoteProcessSnapshot {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        let query = [
            URLQueryItem(name: "ts", value: f.string(from: ts)),
            URLQueryItem(name: "by", value: by.rawValue),
            URLQueryItem(name: "limit", value: String(limit)),
        ]
        var c = URLComponents()
        c.queryItems = query
        let data = try await raw("GET", "/v1/nodes/\(nodeID.uuidString)/processes/at?" + (c.percentEncodedQuery ?? ""))
        do {
            var snapshot = try SampleCodec.decoder().decode(RemoteProcessSnapshot.self, from: data)
            snapshot.bytes = data.count
            return snapshot
        } catch {
            throw RemoteError.decode(String(describing: error))
        }
    }

    func offenders(
        nodeID: UUID, from: Date, to: Date, by: ProcSort, limit: Int, series: Int
    ) async throws -> RemoteOffenders {
        let query = Self.rangeItems(from: from, to: to) + [
            URLQueryItem(name: "by", value: by.rawValue),
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "series", value: String(series)),
        ]
        return try await get("/v1/nodes/\(nodeID.uuidString)/processes", query: query, decoder: SampleCodec.decoder())
    }

    func process(nodeID: UUID, key: String, from: Date, to: Date, step: Duration) async throws -> RemoteProcessDetail {
        let query = Self.rangeItems(from: from, to: to) + [
            URLQueryItem(name: "step", value: "\(step.components.seconds)s"),
        ]
        let safe = key.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._~"))) ?? key
        return try await get(
            "/v1/nodes/\(nodeID.uuidString)/processes/\(safe)", query: query, decoder: SampleCodec.decoder()
        )
    }

    func diskHistory(nodeID: UUID, from: Date, to: Date) async throws -> RemoteDiskHistory {
        try await get("/v1/nodes/\(nodeID.uuidString)/disks", query: Self.rangeItems(from: from, to: to),
                      decoder: SampleCodec.decoder())
    }

    private static let fleetGroupSize = 200

    private static let rangeFormatter: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    private static func rangeItems(from: Date, to: Date) -> [URLQueryItem] {
        [
            URLQueryItem(name: "from", value: rangeFormatter.string(from: from)),
            URLQueryItem(name: "to", value: rangeFormatter.string(from: to)),
        ]
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

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = [], decoder: JSONDecoder? = nil) async throws -> T {
        var full = path
        if !query.isEmpty {
            var c = URLComponents()
            c.queryItems = query
            full += "?" + (c.percentEncodedQuery ?? "")
        }
        let data = try await raw("GET", full)
        do { return try (decoder ?? self.decoder).decode(T.self, from: data) }
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
            var errors = StreamErrorLog()
            while !Task.isCancelled {
                do {
                    try await self.streamOnce(stream: stream, lease: lease, onConnect: onConnect, onMessage: onMessage)
                } catch {
                    if !Task.isCancelled, errors.shouldLog(error, now: Date()) {
                        await Logger.shared.warn("stream: connection failed: \(StreamErrorLog.text(error))", category: "cloud")
                    }
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
        // URLSession offers permessage-deflate, then fails with "Protocol error" on the
        // frame after a deflated frame. No offer gives plain frames.
        req.setValue("", forHTTPHeaderField: "Sec-WebSocket-Extensions")
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

/// Keeps the retry loop of the stream from a flood of log lines. It logs a
/// new kind of error at once, and the same kind again after `repeatEvery`.
struct StreamErrorLog: Sendable {
    static let repeatEvery: TimeInterval = 600

    private var lastKey: String?
    private var lastAt: Date?

    mutating func shouldLog(_ error: Error, now: Date) -> Bool {
        let key = Self.key(error)
        if key == lastKey, let lastAt, now.timeIntervalSince(lastAt) < Self.repeatEvery { return false }
        lastKey = key
        lastAt = now
        return true
    }

    static func text(_ error: Error) -> String {
        "\(key(error)): \(error.localizedDescription)"
    }

    /// The domain and code only: the user info changes at each try.
    private static func key(_ error: Error) -> String {
        let ns = error as NSError
        return "\(ns.domain) \(ns.code)"
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

private struct EmptyBody: Encodable {}

struct RemoteAlert: Decodable, Sendable {
    let id: UUID
    let nodeId: UUID
    let metric: String
    let tint: String
    let ackedBy: UUID?
    let snoozedUntil: Date?

    enum CodingKeys: String, CodingKey {
        case id
        case nodeId = "node_id"
        case metric, tint
        case ackedBy = "acked_by"
        case snoozedUntil = "snoozed_until"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        nodeId = try c.decode(UUID.self, forKey: .nodeId)
        metric = try c.decode(String.self, forKey: .metric)
        tint = try c.decode(String.self, forKey: .tint)
        ackedBy = try c.decodeIfPresent(UUID.self, forKey: .ackedBy)
        // The server sends fractional seconds, which `.iso8601` rejects.
        snoozedUntil = try c.decodeIfPresent(String.self, forKey: .snoozedUntil).flatMap(SampleCodec.parseRFC3339UTC)
    }

    /// True while an ack or a snooze silences the alert.
    func isSilenced(at now: Date = Date()) -> Bool {
        ackedBy != nil || (snoozedUntil.map { $0 > now } ?? false)
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
    /// Read from an old server. A request never sends it.
    var customThresholds: MetricThresholds?
    var snoozedUntil: Date?
    var favorite: Bool
    /// Nil when the server is older than disk scans.
    var allowDiskScan: Bool?
    /// True keeps the process folders on the host. Nil when the server is
    /// older than the switch; an update with nil keeps the stored value.
    var hideProcFolders: Bool?
    /// Nil when the server is older than long-running process alerts.
    var customLongProc: LongProcRule?
    var ignoredProcs: [String]?
    /// The rules of the host (docs/wire.md). Nil when the server is older.
    var thresholdOverrides: ThresholdOverrides?
    var alertOverrides: RemoteAlertOverrides?
    var mutedSignals: MutedSignals?
    var rulesOrigin: RemoteRulesOrigin?

    enum CodingKeys: String, CodingKey {
        case id, displayName, kind, sshUser, sshHost, tags, enabled
        case iconOnWarn, iconOnCritical, notifyOnWarn, notifyOnCritical
        case customThresholds, snoozedUntil, favorite, allowDiskScan, hideProcFolders
        case customLongProc, ignoredProcs
        case thresholdOverrides, alertOverrides, mutedSignals, rulesOrigin
    }

    /// An unset `thresholdOverrides` goes out as `null`: the server keeps
    /// the stored value when the key is missing, and clears it on `null`.
    /// The other rule keys are sent only when set.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(displayName, forKey: .displayName)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(sshUser, forKey: .sshUser)
        try c.encodeIfPresent(sshHost, forKey: .sshHost)
        try c.encode(tags, forKey: .tags)
        try c.encode(enabled, forKey: .enabled)
        try c.encode(iconOnWarn, forKey: .iconOnWarn)
        try c.encode(iconOnCritical, forKey: .iconOnCritical)
        try c.encode(notifyOnWarn, forKey: .notifyOnWarn)
        try c.encode(notifyOnCritical, forKey: .notifyOnCritical)
        try c.encodeIfPresent(snoozedUntil, forKey: .snoozedUntil)
        try c.encode(favorite, forKey: .favorite)
        try c.encodeIfPresent(allowDiskScan, forKey: .allowDiskScan)
        try c.encodeIfPresent(hideProcFolders, forKey: .hideProcFolders)
        try c.encodeIfPresent(customLongProc, forKey: .customLongProc)
        try c.encodeIfPresent(ignoredProcs, forKey: .ignoredProcs)
        if let thresholdOverrides {
            try c.encode(thresholdOverrides, forKey: .thresholdOverrides)
        } else {
            try c.encodeNil(forKey: .thresholdOverrides)
        }
        try c.encodeIfPresent(alertOverrides, forKey: .alertOverrides)
        try c.encodeIfPresent(mutedSignals, forKey: .mutedSignals)
        try c.encodeIfPresent(rulesOrigin, forKey: .rulesOrigin)
    }
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
            thresholdOverrides: r.thresholdOverrides ?? r.customThresholds.map(ThresholdOverrides.init(legacy:)),
            customLongProc: r.customLongProc,
            ignoredProcs: r.ignoredProcs ?? [],
            mutedSignals: r.mutedSignals ?? .empty,
            snoozedUntil: r.snoozedUntil,
            favorite: r.favorite,
            allowDiskScan: r.allowDiskScan ?? false,
            sendProcFolders: !(r.hideProcFolders ?? false)
        )
    }

    /// Sends the host overrides and mutes as they are, so `global` has no
    /// use now. `hideProcFolders` is sent only when it is not nil.
    func toRemote(global: MetricThresholds, hideProcFolders: Bool? = nil) -> RemoteNode {
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
            customThresholds: nil,
            snoozedUntil: snoozedUntil,
            favorite: favorite,
            allowDiskScan: allowDiskScan,
            hideProcFolders: hideProcFolders,
            customLongProc: customLongProc,
            ignoredProcs: ignoredProcs,
            thresholdOverrides: thresholdOverrides,
            mutedSignals: mutedSignals
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
