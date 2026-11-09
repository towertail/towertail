import Foundation

/// Adds a process name to an ignore list, saves it through the backend and
/// pushes the new list to each host at once.
@MainActor
struct LongProcIgnore {
    enum Scope {
        case allHosts
        case host(UUID)
    }

    let settings: ServerSettings
    let nodes: NodeStore
    let servers: ServerStore
    let backend: (any Backend)?

    func ignore(_ name: String, on scope: Scope) async {
        do {
            switch scope {
            case .allHosts:
                guard !settings.longProc.ignoreSet().contains(name.lowercased()) else { return }
                settings.longProc.ignore.append(name)
                if let backend { try await backend.updateServerSettings(settings) } else { settings.persist() }
            case .host(let id):
                guard var node = nodes.node(withId: id),
                      !Set(node.ignoredProcs.map { $0.lowercased() }).contains(name.lowercased())
                else { return }
                node.ignoredProcs.append(name)
                if let backend { try await backend.updateNode(node) } else { nodes.update(node) }
            }
        } catch {
            Logger.shared.warn("longproc: ignore failed: \(error)", category: "settings", kv: ["name": name])
        }
        for vm in servers.serverVMs {
            if let node = nodes.node(withId: vm.id) { vm.applyLongProc(node: node, global: settings.longProc) }
        }
    }
}
