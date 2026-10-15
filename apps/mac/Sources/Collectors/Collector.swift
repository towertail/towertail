import Foundation

protocol Collector: Sendable {
    func run(sink: ServerStore) async
    /// Drops the cached pacer entry for a node so the next supervisor
    /// tick spawns a fresh one. Used by the Preferences "Test" button to
    /// resume polling after the user fixes a host that previously halted
    /// (auth fix, sampler push, etc.) without forcing them to disable
    /// and re-enable the node.
    func respawnPacer(id: UUID)
}
