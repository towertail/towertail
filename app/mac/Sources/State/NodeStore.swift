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
    }

    /// Appends multiple nodes in one shot and persists once at the end.
    /// A 20-node bulk import would otherwise hit disk 20 times and flash
    /// the UI through 20 intermediate states.
    func addMany(_ newNodes: [Node]) {
        guard !newNodes.isEmpty else { return }
        nodes.append(contentsOf: newNodes)
        persist()
    }

    func remove(id: UUID) {
        nodes.removeAll { $0.id == id }
        persist()
    }

    func update(_ node: Node) {
        if let i = nodes.firstIndex(where: { $0.id == node.id }) {
            nodes[i] = node
            persist()
        }
    }

    func setEnabled(id: UUID, enabled: Bool) {
        if let i = nodes.firstIndex(where: { $0.id == id }) {
            nodes[i].enabled = enabled
            persist()
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
        }
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
