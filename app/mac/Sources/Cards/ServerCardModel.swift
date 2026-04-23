import Foundation

/// Bundles the node lookup + backend mutations a `ServerCardView` needs
/// into a single value. Exists so the card body doesn't open-code the
/// `let b = backend; let id = vm.id; Task { try? await b?.foo(...) }`
/// pattern five times — and so adding a new per-node action only touches
/// one file.
@MainActor
struct ServerCardModel {
    let node: Node?
    let isUpdatingSampler: Bool

    /// Captured weakly via the optional protocol type so the closures
    /// don't keep the backend alive past the card's lifetime.
    private let backend: (any Backend)?

    init(
        nodeID: UUID,
        nodeStore: NodeStore,
        samplerUpdater: SamplerUpdateCoordinator,
        backend: (any Backend)?
    ) {
        self.node = nodeStore.node(withId: nodeID)
        self.isUpdatingSampler = samplerUpdater.isUpdating(id: nodeID)
        self.backend = backend
    }

    var isSnoozed: Bool { node?.isSnoozed == true }
    var isFavorite: Bool { node?.favorite == true }

    func snooze(id: UUID, until: Date?) {
        let b = backend
        Task { try? await b?.setNodeSnooze(id: id, until: until) }
    }

    func toggleFavorite(id: UUID, currentlyFavorite: Bool) {
        let b = backend
        let next = !currentlyFavorite
        Task { try? await b?.setNodeFavorite(id: id, favorite: next) }
    }
}
