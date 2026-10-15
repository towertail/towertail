import Foundation

/// Wraps today's in-process collector stack — RealCollector, ServerStore,
/// NodeStore, HistoryStore, ThresholdNotifier, SamplerUpdateCoordinator,
/// SystemReachabilityMonitor — behind the `Backend` protocol. Behavior
/// is identical to the pre-refactor `AppEnvironment`: the split is
/// structural, not functional.
@MainActor
final class LocalBackend: Backend {
    let servers: ServerStore
    let nodes: NodeStore
    let clientSettings: ClientSettings
    let serverSettings: ServerSettings
    let samplerUpdater: SamplerUpdateCoordinator

    let history: HistoryStore
    let collector: any Collector
    let notifier: ThresholdNotifier
    let reachability: SystemReachabilityMonitor

    private var task: Task<Void, Never>?

    init() {
        let clientSettings = ClientSettings.loadFromDisk()
        let serverSettings = ServerSettings.loadFromDisk()
        let nodeStore = NodeStore.loadFromDisk()
        let history = HistoryStore(url: HistoryStore.defaultURL())
        self.clientSettings = clientSettings
        self.serverSettings = serverSettings
        self.nodes = nodeStore
        self.history = history
        HostKeyTrustPersister.bind(store: nodeStore)

        let servers = ServerStore(
            history: history,
            nodeLookup: { [weak nodeStore] id in nodeStore?.node(withId: id) }
        )
        self.servers = servers

        let manifest = SamplerManifestLoader.load()
        let updater = SamplerUpdateCoordinator(manifest: manifest)
        self.samplerUpdater = updater

        let reachability = SystemReachabilityMonitor(settings: serverSettings)
        self.reachability = reachability

        self.collector = RealCollector(
            nodeStore: nodeStore,
            settings: serverSettings,
            history: history,
            samplerUpdater: updater,
            reachability: reachability
        )
        let notifier = ThresholdNotifier(settings: serverSettings, reachability: reachability)
        self.notifier = notifier
        servers.notifier = notifier

        let expected = manifest?.expectedSamplerField ?? "(none)"
        Logger.shared.info(
            "app: launch",
            category: "lifecycle",
            kv: [
                "nodes": String(nodeStore.nodes.count),
                "bundled_sampler": expected,
                "auto_update": String(serverSettings.autoUpdateSamplersEnabled),
            ]
        )
    }

    func start() {
        guard task == nil else { return }
        Logger.shared.info("collector: starting", category: "lifecycle")
        let collector = self.collector
        let servers = self.servers
        reachability.start()
        notifier.start()
        task = Task.detached(priority: .utility) {
            await collector.run(sink: servers)
        }
    }

    func stop() {
        Logger.shared.info("collector: stopping", category: "lifecycle")
        task?.cancel()
        task = nil
        reachability.stop()
    }

    // MARK: - Node CRUD

    func addNode(_ node: Node) async throws {
        nodes.add(node)
    }

    func addNodes(_ newNodes: [Node]) async throws {
        nodes.addMany(newNodes)
    }

    func updateNode(_ node: Node) async throws {
        nodes.update(node)
    }

    func removeNode(id: UUID) async throws {
        nodes.remove(id: id)
    }

    func setNodeEnabled(id: UUID, enabled: Bool) async throws {
        nodes.setEnabled(id: id, enabled: enabled)
    }

    func setNodeSnooze(id: UUID, until: Date?) async throws {
        nodes.setSnooze(id: id, until: until)
    }

    func setNodeFavorite(id: UUID, favorite: Bool) async throws {
        nodes.setFavorite(id: id, favorite: favorite)
    }

    func respawnPacer(id: UUID) async {
        collector.respawnPacer(id: id)
    }

    func updateServerSettings(_ settings: ServerSettings) async throws {
        // ServerSettings is already the canonical @Observable — Local
        // callers mutate it directly and call persist(). This method
        // exists for protocol symmetry with Remote, which will POST the
        // payload to the server and refresh its local copy on success.
        settings.persist()
    }
}
