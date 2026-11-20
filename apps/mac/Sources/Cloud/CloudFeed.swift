import Foundation
import Observation

/// Takes the data of Cloud hosts from `/v1/stream`. Hosts without a Cloud
/// link stay on SSH polling.
@MainActor
@Observable
final class CloudFeed {
    private(set) var connected = false
    /// Nodes with an open reachability alert.
    private(set) var alerted: Set<UUID> = []

    @ObservationIgnored private let client: RemoteClient
    @ObservationIgnored private let servers: ServerStore
    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let viewLease: ViewLeaseController?
    @ObservationIgnored private var stream: RemoteStream?
    /// Reachability events since the last connect. They win over the
    /// open alerts, which can be older than the events.
    @ObservationIgnored private var eventAlerts: [UUID: Bool] = [:]

    init(client: RemoteClient, servers: ServerStore, nodes: NodeStore, viewLease: ViewLeaseController? = nil) {
        self.client = client
        self.servers = servers
        self.nodes = nodes
        self.viewLease = viewLease
    }

    func start() {
        guard stream == nil else { return }
        stream = client.openStream(
            lease: true,
            onConnect: { [weak self] in
                Task { @MainActor in
                    guard let self else { return }
                    self.connected = true
                    self.eventAlerts = [:]
                    self.viewLease?.streamDidConnect()
                    await self.syncNodes()
                    await self.loadAlerts()
                }
            },
            onDisconnect: { [weak self] in
                Task { @MainActor in self?.connected = false }
            },
            onMessage: { [weak self] raw in
                Task { @MainActor in self?.handle(raw) }
            }
        )
    }

    func stop() {
        stream?.cancel()
        stream = nil
        connected = false
        // Without the feed no data comes, so the stale check takes over.
        for vm in servers.serverVMs where vm.liveSource == .cloud { vm.liveSource = .poll }
    }

    /// Sends a `view` frame. The stream drops it when there is no connection.
    func send(_ view: LiveView) {
        guard let stream else { return }
        Task { await stream.send(view) }
    }

    /// Adds hosts that only Cloud knows. Removes the Cloud link of hosts
    /// that Cloud dropped.
    func syncNodes() async {
        let remote: [RemoteNode]
        do {
            remote = try await client.listNodes()
        } catch {
            Logger.shared.warn("cloud: node list failed: \(error)", category: "cloud")
            return
        }
        let remoteIDs = Set(remote.map(\.id))
        for r in remote where nodes.node(withId: r.id) == nil {
            nodes.add(Node(id: r.id, displayName: r.displayName, kind: .cloud, cloud: CloudLink(enrolledAt: Date())))
        }
        for node in nodes.nodes where node.isCloud && !remoteIDs.contains(node.id) {
            Logger.shared.info("cloud: host removed in Cloud", category: "cloud", hostID: node.id, host: node.displayName)
            if node.kind == .cloud {
                nodes.remove(id: node.id)
            } else {
                var local = node
                local.cloud = nil
                nodes.update(local)
            }
        }
    }

    func handle(_ raw: String) {
        switch StreamMessage.decode(raw) {
        case .sample(let id, let sample):
            guard nodes.node(withId: id)?.isCloud == true else { return }
            servers.ingest(sample, for: id)
        case .summary(let id, let summary):
            guard nodes.node(withId: id)?.isCloud == true else { return }
            servers.ingest(summary: summary, for: id)
        case .detail(let id, let tab, let detail):
            guard nodes.node(withId: id)?.isCloud == true else { return }
            servers.apply(detail: detail, tab: tab, for: id)
        case .event(let event):
            guard event.metric == "reachability" else { return }
            if nodes.node(withId: event.nodeId)?.isCloud == true { servers.apply(reachability: event) }
            let open = event.tint == "critical"
            eventAlerts[event.nodeId] = open
            if open { alerted.insert(event.nodeId) } else { alerted.remove(event.nodeId) }
        case .nodeUpdated(let remote):
            guard var node = nodes.node(withId: remote.id), node.isCloud,
                  node.displayName != remote.displayName else { return }
            node.displayName = remote.displayName
            nodes.update(node)
        case .hello:
            // The server runs in lease mode: no data comes while no view is open.
            for vm in servers.serverVMs where nodes.node(withId: vm.id)?.isCloud == true {
                vm.liveSource = .cloud
            }
        case .lease(let notice):
            if notice.isLimit { viewLease?.didReceiveLimit() }
        case .settingsUpdated, .diskScan, .other:
            break
        }
    }

    /// Seeds `alerted` with the alerts that opened before the connection.
    private func loadAlerts() async {
        guard let open = try? await client.openAlerts() else { return }
        var ids = Set(open.filter { $0.metric == "reachability" && $0.tint == "critical" }.map(\.nodeId))
        for (id, isOpen) in eventAlerts {
            if isOpen { ids.insert(id) } else { ids.remove(id) }
        }
        alerted = ids
    }
}
