import Foundation
import Observation

/// Keeps the last rules of the cloud on disk. The app uses them until the
/// first fetch ends.
struct CloudRulesCache {
    let url: URL

    static func standard() -> CloudRulesCache {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return CloudRulesCache(url: base.appendingPathComponent("Towertail", isDirectory: true)
            .appendingPathComponent("cloud-rules.json"))
    }

    func load() -> RemoteRulesView? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(RemoteRulesView.self, from: data)
    }

    func save(_ view: RemoteRulesView) {
        guard let data = try? JSONEncoder().encode(view) else { return }
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? data.write(to: url, options: .atomic)
    }

    func clear() { try? FileManager.default.removeItem(at: url) }
}

/// The rules of the cloud: the org rules and the rules of each host. A cloud
/// host uses these in place of the local rules of the Mac.
@MainActor
@Observable
final class CloudRules: CloudRulesSource {
    private(set) var view: RemoteRulesView?
    private(set) var error: String?

    @ObservationIgnored private let client: RemoteClient
    @ObservationIgnored private let device: String
    @ObservationIgnored private let cache: CloudRulesCache?
    @ObservationIgnored private let gap: TimeInterval
    @ObservationIgnored private var byNode: [UUID: RemoteRulesView.Host] = [:]
    @ObservationIgnored private var pending: Task<Void, Never>?
    @ObservationIgnored private var lastFetch = Date.distantPast

    init(client: RemoteClient, device: String, cache: CloudRulesCache? = nil, gap: TimeInterval = 2) {
        self.client = client
        self.device = device
        self.cache = cache
        self.gap = gap
        if let cached = cache?.load() { apply(cached) }
    }

    var org: RemoteOrgRules? { view?.org }

    func effective(for nodeID: UUID) -> RemoteEffectiveRules? { byNode[nodeID]?.effective }

    func host(_ nodeID: UUID) -> RemoteHostRules? { byNode[nodeID]?.host }

    func refresh() async {
        lastFetch = Date()
        do {
            let fresh = try await client.rules()
            apply(fresh)
            cache?.save(fresh)
            error = nil
        } catch {
            self.error = error.localizedDescription
            Logger.shared.warn("cloud: rules fetch failed: \(error)", category: "cloud")
        }
    }

    /// Fetches at once, or after the gap when the last fetch is recent.
    /// Calls during the wait share one fetch.
    func refreshSoon() {
        guard pending == nil else { return }
        let wait = max(0, gap - Date().timeIntervalSince(lastFetch))
        pending = Task { [weak self] in
            if wait > 0 { try? await Task.sleep(for: .seconds(wait)) }
            guard let self, !Task.isCancelled else { return }
            self.pending = nil
            await self.refresh()
        }
    }

    func stop() {
        pending?.cancel()
        pending = nil
    }

    func saveHost(_ nodeID: UUID, _ rules: RemoteHostRules) async throws {
        var body = rules
        body.origin = RemoteRulesOrigin(client: "mac", device: device)
        try await client.putHostRules(nodeID: nodeID, body)
        await refresh()
    }

    func setMute(_ metric: ThresholdMetric, nodeID: UUID?, muted: Bool) async throws {
        try await sendMute(metric.rawValue, nodeID: nodeID, muted: muted)
    }

    func setLongMute(nodeID: UUID?, muted: Bool) async throws {
        try await sendMute(MutedSignals.longName, nodeID: nodeID, muted: muted)
    }

    private func sendMute(_ name: String, nodeID: UUID?, muted: Bool) async throws {
        try await client.setMute(name, nodeID: nodeID, muted: muted)
        await refresh()
    }

    private func apply(_ v: RemoteRulesView) {
        view = v
        byNode = Dictionary(v.hosts.map { ($0.nodeId, $0) }, uniquingKeysWith: { _, last in last })
    }
}
