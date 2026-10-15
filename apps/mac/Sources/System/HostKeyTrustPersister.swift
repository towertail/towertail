import Foundation

/// Bridges the TOFU prompt accept path back to the live `NodeStore`, which
/// is `@MainActor`. Held globally because the SSH connection factory runs
/// under a Sendable closure and can't capture a specific instance.
@MainActor
enum HostKeyTrustPersister {
    private static weak var store: NodeStore?

    static func bind(store: NodeStore) {
        self.store = store
    }

    /// Write the freshly-accepted fingerprint onto the matching node. Does
    /// nothing if no store is bound yet (tests, early startup) or the node
    /// has already been removed.
    static func persist(nodeId: UUID, fingerprint: String) {
        guard let store, var existing = store.node(withId: nodeId) else { return }
        existing.knownHostFingerprint = fingerprint
        store.update(existing)
    }
}
