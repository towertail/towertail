import Foundation

/// Phase 3 stub. Compiles and conforms to `Backend` so the seam is in
/// place, but nothing is wired — no transport, no URL, no token. All
/// mutators throw `BackendError.notImplemented`; observable state is
/// empty and never changes. Flipping `BackendMode` to `.remote` will
/// light up a functional client in a future change.
@MainActor
final class RemoteBackend: Backend {
    let servers: ServerStore
    let nodes: NodeStore
    let clientSettings: ClientSettings
    let serverSettings: ServerSettings
    let samplerUpdater: SamplerUpdateCoordinator

    init() {
        self.servers = ServerStore()
        self.nodes = NodeStore(nodes: [])
        self.clientSettings = ClientSettings()
        self.serverSettings = ServerSettings()
        // Idle updater — in Remote mode the server owns sampler push;
        // the client only displays version status received over the
        // event stream (once that ships).
        self.samplerUpdater = SamplerUpdateCoordinator(manifest: nil)
    }

    func start() {
        Logger.shared.info(
            "remote: not yet implemented",
            category: "lifecycle"
        )
    }

    func stop() {
        // no-op
    }

    func addNode(_ node: Node) async throws { throw BackendError.notImplemented }
    func addNodes(_ nodes: [Node]) async throws { throw BackendError.notImplemented }
    func updateNode(_ node: Node) async throws { throw BackendError.notImplemented }
    func removeNode(id: UUID) async throws { throw BackendError.notImplemented }
    func setNodeEnabled(id: UUID, enabled: Bool) async throws { throw BackendError.notImplemented }
    func setNodeSnooze(id: UUID, until: Date?) async throws { throw BackendError.notImplemented }
    func setNodeFavorite(id: UUID, favorite: Bool) async throws { throw BackendError.notImplemented }
    func updateServerSettings(_ settings: ServerSettings) async throws { throw BackendError.notImplemented }
}
