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

    func node(withId id: UUID) -> Node? {
        nodes.first(where: { $0.id == id })
    }

    private func persist() {
        var s = SettingsPersistence.load(from: url)
        s.nodes = nodes
        SettingsPersistence.save(s, to: url)
    }
}
