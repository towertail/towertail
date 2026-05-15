import Foundation

/// Phase 3 remote backend. Implements the `Backend` protocol against a
/// Towertail server: REST for CRUD / settings / kill-process, WebSocket
/// for live samples and events. The seam was laid down in
/// `docs/backend-split.md`; the wire contract is in `docs/wire.md`.
@MainActor
final class RemoteBackend: Backend {
    let servers: ServerStore
    let nodes: NodeStore
    let clientSettings: ClientSettings
    let serverSettings: ServerSettings
    let samplerUpdater: SamplerUpdateCoordinator

    private let client: RemoteClient
    private var streamTask: Task<Void, Never>?

    init(endpoint: URL, token: String) {
        self.servers = ServerStore()
        self.nodes = NodeStore(nodes: [])
        self.clientSettings = ClientSettings()
        self.serverSettings = ServerSettings()
        // In remote mode the server pushes sampler updates to hosts — the
        // client only shows status. Keep the updater idle.
        self.samplerUpdater = SamplerUpdateCoordinator(manifest: nil)
        self.client = RemoteClient(config: .init(endpoint: endpoint, token: token))
    }

    func start() {
        Logger.shared.info("remote: starting", category: "lifecycle")
        Task { [weak self] in
            await self?.initialLoad()
            await self?.openStream()
        }
    }

    func stop() {
        streamTask?.cancel()
        streamTask = nil
    }

    // MARK: - Initial load

    private func initialLoad() async {
        do {
            let remoteNodes = try await client.listNodes()
            let mapped = remoteNodes.map(Node.init(remote:))
            nodes.replaceAll(with: mapped)
            for node in mapped {
                let vm = ServerViewModel(
                    id: node.id,
                    hostname: node.displayName,
                    dnsName: node.userAtHost,
                    osArch: "",
                    kind: node.kind,
                    thresholds: MetricThresholds.effective(
                        global: serverSettings.thresholds,
                        override: node.customThresholds
                    )
                )
                servers.register(vm)
            }
            let rs = try await client.getSettings()
            applyRemoteSettings(rs)
        } catch {
            Logger.shared.warn("remote: initial load failed: \(error)", category: "lifecycle")
        }
    }

    private func openStream() async {
        streamTask?.cancel()
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let sampleDecoder = SampleCodec.decoder()
        streamTask = client.openStream { [weak self] raw in
            guard let self else { return }
            Task { @MainActor in
                self.handleStreamMessage(raw, decoder: decoder, sampleDecoder: sampleDecoder)
            }
        }
    }

    private func handleStreamMessage(_ raw: String, decoder: JSONDecoder, sampleDecoder: JSONDecoder) {
        guard let data = raw.data(using: .utf8) else { return }
        struct Envelope: Decodable {
            let type: String
            let nodeId: UUID?
        }
        guard let env = try? decoder.decode(Envelope.self, from: data) else { return }
        switch env.type {
        case "sample":
            guard let id = env.nodeId else { return }
            struct SampleWrap: Decodable {
                let sample: Sample
                enum CodingKeys: String, CodingKey { case sample }
                init(from d: Decoder) throws {
                    let c = try d.container(keyedBy: CodingKeys.self)
                    self.sample = try c.decode(Sample.self, forKey: .sample)
                }
            }
            // Re-decode with the custom sample codec so timestamps parse.
            do {
                let wrap = try sampleDecoder.decode(SampleWrap.self, from: data)
                servers.ingest(wrap.sample, for: id)
            } catch {
                Logger.shared.debug("remote: sample decode failed: \(error)", category: "collector")
            }
        case "node_updated":
            struct NodeWrap: Decodable { let node: RemoteNode }
            if let wrap = try? decoder.decode(NodeWrap.self, from: data) {
                let n = Node(remote: wrap.node)
                nodes.update(n)
            }
        case "settings_updated":
            struct SettingsWrap: Decodable { let settings: RemoteServerSettings }
            if let wrap = try? decoder.decode(SettingsWrap.self, from: data) {
                applyRemoteSettings(wrap.settings)
            }
        case "event":
            // Phase H: thread BackendEvents into the notifier once the
            // remote dispatcher lands. For now log and move on.
            Logger.shared.info("remote: event", category: "alerts", kv: ["raw": raw])
        default:
            break
        }
    }

    private func applyRemoteSettings(_ rs: RemoteServerSettings) {
        serverSettings.thresholds = rs.thresholds
        serverSettings.localPollingIntervalSeconds = rs.localPollingIntervalSeconds
        serverSettings.sshPollingIntervalSeconds = rs.sshPollingIntervalSeconds
        serverSettings.notificationsEnabled = rs.notificationsEnabled
        serverSettings.notifyWarn = rs.notifyWarn
        serverSettings.notifyCritical = rs.notifyCritical
        serverSettings.notifyDebounceSeconds = rs.notifyDebounceSeconds
        serverSettings.autoUpdateSamplersEnabled = rs.autoUpdateSamplersEnabled
        serverSettings.postWakeGraceSeconds = rs.postWakeGraceSeconds
    }

    // MARK: - Backend mutations

    func addNode(_ node: Node) async throws {
        let created = try await client.createNode(node.toRemote())
        nodes.add(Node(remote: created))
    }

    func addNodes(_ newNodes: [Node]) async throws {
        for n in newNodes { try await addNode(n) }
    }

    func updateNode(_ node: Node) async throws {
        let updated = try await client.updateNode(node.toRemote())
        nodes.update(Node(remote: updated))
    }

    func removeNode(id: UUID) async throws {
        try await client.deleteNode(id: id)
        nodes.remove(id: id)
    }

    func setNodeEnabled(id: UUID, enabled: Bool) async throws {
        guard var n = nodes.node(withId: id) else { return }
        n.enabled = enabled
        try await updateNode(n)
    }

    func setNodeSnooze(id: UUID, until: Date?) async throws {
        guard var n = nodes.node(withId: id) else { return }
        n.snoozedUntil = until
        try await updateNode(n)
    }

    func setNodeFavorite(id: UUID, favorite: Bool) async throws {
        guard var n = nodes.node(withId: id) else { return }
        n.favorite = favorite
        try await updateNode(n)
    }

    func respawnPacer(id: UUID) async {
        // Remote mode: the server owns the polling loop; the client has
        // no in-process collector to poke. Phase-3 server should expose
        // an equivalent endpoint so the Test button has the same effect
        // there. Until then this is a no-op.
    }

    func updateServerSettings(_ settings: ServerSettings) async throws {
        let rs = RemoteServerSettings(
            thresholds: settings.thresholds,
            localPollingIntervalSeconds: settings.localPollingIntervalSeconds,
            sshPollingIntervalSeconds: settings.sshPollingIntervalSeconds,
            notificationsEnabled: settings.notificationsEnabled,
            notifyWarn: settings.notifyWarn,
            notifyCritical: settings.notifyCritical,
            notifyDebounceSeconds: settings.notifyDebounceSeconds,
            autoUpdateSamplersEnabled: settings.autoUpdateSamplersEnabled,
            postWakeGraceSeconds: settings.postWakeGraceSeconds
        )
        let saved = try await client.putSettings(rs)
        applyRemoteSettings(saved)
    }
}
