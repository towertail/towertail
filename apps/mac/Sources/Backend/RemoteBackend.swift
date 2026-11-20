import AppKit
import Foundation

/// Phase 3 remote backend. Implements the `Backend` protocol against a
/// Towertail server: REST for CRUD / settings / kill-process, WebSocket
/// for live samples and events. The seam was laid down in
/// `docs/archive/backend-split.md`; the wire contract is in `docs/wire.md`.
@MainActor
final class RemoteBackend: Backend {
    let servers: ServerStore
    let nodes: NodeStore
    let clientSettings: ClientSettings
    let serverSettings: ServerSettings
    let samplerUpdater: SamplerUpdateCoordinator
    let diskScans: DiskScanStore
    /// Changes of server scan jobs, for `RemoteDiskScanner`.
    let scanJobs = DiskScanJobFeed()

    /// Reports what the user looks at.
    private(set) var viewLease: ViewLeaseController!

    private let client: RemoteClient
    private var stream: RemoteStream?
    /// True after a `hello` frame: the server runs in lease mode.
    private(set) var leaseMode = false

    init(endpoint: URL, token: String) {
        self.servers = ServerStore()
        self.nodes = NodeStore(nodes: [])
        self.clientSettings = ClientSettings()
        self.serverSettings = ServerSettings()
        // In remote mode the server pushes sampler updates to hosts — the
        // client only shows status. Keep the updater idle.
        self.samplerUpdater = SamplerUpdateCoordinator(manifest: nil)
        self.client = RemoteClient(config: .init(endpoint: endpoint, token: token))
        self.diskScans = DiskScanStore(url: HistoryStore.defaultURL())
        self.viewLease = ViewLeaseController(send: { [weak self] view in
            guard let stream = self?.stream else { return }
            Task { await stream.send(view) }
        })
    }

    func start() {
        Logger.shared.info("remote: starting", category: "lifecycle")
        viewLease.startObservingSleep()
        Task { [weak self] in
            await self?.initialLoad()
            await self?.openStream()
        }
    }

    func stop() {
        viewLease.stopObservingSleep()
        stream?.cancel()
        stream = nil
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
                        override: node.thresholdOverrides
                    ),
                    alertRules: node.customAlerts ?? serverSettings.alertRules
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
        stream?.cancel()
        let onConnect: @Sendable () -> Void = { [weak self] in
            Task { @MainActor in
                guard let self else { return }
                self.viewLease.streamDidConnect()
                await self.scanJobs.refetch(using: self.client)
            }
        }
        stream = client.openStream(lease: true, onConnect: onConnect) { [weak self] raw in
            guard let self else { return }
            Task { @MainActor in
                self.handleStreamMessage(raw)
            }
        }
    }

    private func handleStreamMessage(_ raw: String) {
        switch StreamMessage.decode(raw) {
        case .sample(let id, let sample):
            servers.ingest(sample, for: id)
        case .nodeUpdated(let remote):
            nodes.update(Node(remote: remote))
            applyLongProc()
        case .settingsUpdated(let rs):
            applyRemoteSettings(rs)
        case .diskScan(let job):
            scanJobs.publish(job)
        case .hello:
            leaseMode = true
            for vm in servers.serverVMs { vm.liveSource = .cloud }
        case .summary(let id, let summary):
            servers.ingest(summary: summary, for: id)
        case .detail(let id, let tab, let detail):
            servers.apply(detail: detail, tab: tab, for: id)
        case .lease(let notice):
            if notice.isLimit { viewLease.didReceiveLimit() }
        case .event(let event):
            if event.kind == "reachability_changed" {
                servers.apply(reachability: event)
            } else {
                // Phase H: thread BackendEvents into the notifier once the
                // remote dispatcher lands. For now log and move on.
                Logger.shared.info("remote: event", category: "alerts", kv: ["raw": raw])
            }
        case .other:
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
        if let rule = rs.longProc { serverSettings.longProc = rule }
        applyLongProc()
    }

    private func applyLongProc() {
        for vm in servers.serverVMs {
            guard let node = nodes.node(withId: vm.id) else { continue }
            vm.applyLongProc(node: node, global: serverSettings.longProc)
        }
    }

    // MARK: - Backend mutations

    func addNode(_ node: Node) async throws {
        let created = try await client.createNode(node.toRemote(global: serverSettings.thresholds))
        nodes.add(Node(remote: created))
    }

    func addNodes(_ newNodes: [Node]) async throws {
        for n in newNodes { try await addNode(n) }
    }

    func updateNode(_ node: Node) async throws {
        let updated = try await client.updateNode(node.toRemote(global: serverSettings.thresholds))
        nodes.update(Node(remote: updated))
        applyLongProc()
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

    func diskScanner(for node: Node) -> any DiskScanner {
        RemoteDiskScanner(service: client, feed: scanJobs)
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
            postWakeGraceSeconds: settings.postWakeGraceSeconds,
            longProc: settings.longProc
        )
        let saved = try await client.putSettings(rs)
        applyRemoteSettings(saved)
    }
}
