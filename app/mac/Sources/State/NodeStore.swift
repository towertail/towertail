import Foundation

@Observable
@MainActor
final class NodeStore {
    private(set) var nodes: [Node]
    private let url: URL

    init(nodes: [Node], url: URL = SettingsPersistence.defaultURL()) {
        self.nodes = nodes
        self.url = url
    }

    static func loadFromDisk(url: URL = SettingsPersistence.defaultURL()) -> NodeStore {
        let settings = SettingsPersistence.load(from: url)
        return NodeStore(nodes: settings.nodes, url: url)
    }

    func add(_ node: Node) {
        nodes.append(node)
        persist()
        Logger.shared.info(
            "server: added", category: "servers",
            hostID: node.id, host: node.displayName,
            kv: ["kind": node.kind.rawValue]
        )
    }

    /// Appends multiple nodes in one shot and persists once at the end.
    /// A 20-node bulk import would otherwise hit disk 20 times and flash
    /// the UI through 20 intermediate states.
    func addMany(_ newNodes: [Node]) {
        guard !newNodes.isEmpty else { return }
        nodes.append(contentsOf: newNodes)
        persist()
        Logger.shared.info(
            "servers: bulk added \(newNodes.count)",
            category: "servers",
            kv: ["count": String(newNodes.count)]
        )
    }

    func remove(id: UUID) {
        let name = node(withId: id)?.displayName ?? id.uuidString.prefix(8).description
        nodes.removeAll { $0.id == id }
        persist()
        Logger.shared.info(
            "server: removed", category: "servers",
            hostID: id, host: name
        )
    }

    func update(_ node: Node) {
        if let i = nodes.firstIndex(where: { $0.id == node.id }) {
            nodes[i] = node
            persist()
            Logger.shared.info(
                "server: updated", category: "servers",
                hostID: node.id, host: node.displayName
            )
        }
    }

    func setEnabled(id: UUID, enabled: Bool) {
        if let i = nodes.firstIndex(where: { $0.id == id }) {
            nodes[i].enabled = enabled
            persist()
            Logger.shared.info(
                "server: \(enabled ? "enabled" : "disabled")",
                category: "servers",
                hostID: id, host: nodes[i].displayName
            )
        }
    }

    /// Sets the node's snooze expiry (or clears it with `nil`). Persists
    /// immediately so an app restart doesn't un-snooze — a user quieting a
    /// noisy disk and getting re-buzzed within minutes would be worse than
    /// no snooze at all.
    func setSnooze(id: UUID, until: Date?) {
        if let i = nodes.firstIndex(where: { $0.id == id }) {
            nodes[i].snoozedUntil = until
            persist()
            Logger.shared.info(
                until == nil ? "server: snooze cleared" : "server: snoozed",
                category: "servers",
                hostID: id, host: nodes[i].displayName,
                kv: until == nil ? [:] : [
                    "until": ISO8601DateFormatter().string(from: until!)
                ]
            )
        }
    }

    /// Stamps the last-successful-connect timestamp on a node and persists.
    /// The pacer calls this only on its first successful sample per
    /// instance — subsequent successes don't re-write so we don't churn
    /// the JSON file every poll. The supervisor's snapshot diff also
    /// won't trip on a no-op stamp (skipping when the field is already
    /// non-nil keeps the supervisor from respawning the pacer).
    func markConnected(id: UUID, at: Date = Date()) {
        if let i = nodes.firstIndex(where: { $0.id == id }) {
            // First-ever connect: stamp + persist (and live with the
            // supervisor respawn — it's a one-time event per node, not a
            // per-poll churn).
            if nodes[i].lastSuccessfulConnect == nil {
                nodes[i].lastSuccessfulConnect = at
                persist()
                Logger.shared.info(
                    "server: first connect", category: "servers",
                    hostID: id, host: nodes[i].displayName
                )
            }
            // For warm hosts we deliberately skip updating the field on
            // every success. The "ever connected" signal is binary; the
            // exact timestamp isn't load-bearing for the menu-bar
            // escalation. Persisting on every poll would churn disk and
            // re-trigger pacer respawns through the snapshot diff.
        }
    }

    func setFavorite(id: UUID, favorite: Bool) {
        if let i = nodes.firstIndex(where: { $0.id == id }) {
            nodes[i].favorite = favorite
            persist()
            Logger.shared.info(
                favorite ? "server: favorited" : "server: unfavorited",
                category: "servers",
                hostID: id, host: nodes[i].displayName
            )
        }
    }

    /// Replaces the entire node list in one shot without re-persisting —
    /// used by settings-import, which has already written the merged
    /// PersistedSettings to disk. Persisting again here would stomp any
    /// fields the importer handled (e.g. per-node thresholdOverrides that
    /// live inside Node).
    func replaceAllFromDisk() {
        let s = SettingsPersistence.load(from: url)
        self.nodes = s.nodes.isEmpty ? [Node.localMac()] : s.nodes
    }

    /// Replaces the entire node list with `newNodes` without touching
    /// local disk. Remote mode uses this when the server is the source
    /// of truth — persisting would fight the `node_updated` WS stream.
    func replaceAll(with newNodes: [Node]) {
        self.nodes = newNodes
    }

    func node(withId id: UUID) -> Node? {
        nodes.first(where: { $0.id == id })
    }

    private func persist() {
        var s = SettingsPersistence.load(from: url)
        s.nodes = nodes
        SettingsPersistence.save(s, to: url)
    }
}
