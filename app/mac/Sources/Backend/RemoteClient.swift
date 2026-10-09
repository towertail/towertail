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

    // MARK: - Transport helpers

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
            if http.statusCode >= 400 {
                let msg = String(data: data, encoding: .utf8) ?? ""
                throw RemoteError.http(http.statusCode, msg)
            }
            return data
        } catch let err as RemoteError {
            throw err
        } catch {
            throw RemoteError.transport(error)
        }
    }

    // MARK: - WebSocket

    /// Opens a WS connection and invokes `onMessage` for each received
    /// text frame. The returned task runs until cancelled.
    func openStream(onMessage: @escaping @Sendable (String) -> Void) -> Task<Void, Never> {
        return Task.detached { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                do {
                    try await self.streamOnce(onMessage: onMessage)
                } catch {
                    // transient; backoff and retry.
                }
                let jitter = UInt64.random(in: 500...1500) * 1_000_000
                try? await Task.sleep(nanoseconds: jitter)
            }
        }
    }

    private func streamOnce(onMessage: @escaping @Sendable (String) -> Void) async throws {
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

        while !Task.isCancelled {
            let message = try await task.receive()
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
            favorite: r.favorite
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
            favorite: favorite
        )
    }
}
