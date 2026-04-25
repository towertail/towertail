import Foundation
import SwiftUI

/// Selects which backend implementation wires up at app launch. Hardcoded
/// to `.local` today. The mode toggle ships with the actual remote server
/// in Phase 3 of `docs/PLAN.md`.
enum BackendMode: Sendable {
    case local
    case remote
}

/// Errors that a backend implementation can raise through async throws
/// entry points. Local mode today is effectively infallible; Remote mode
/// will surface transport failures here.
enum BackendError: Error, LocalizedError {
    case notImplemented

    var errorDescription: String? {
        switch self {
        case .notImplemented:
            return "Not implemented — Phase 3 (remote backend) is still a stub."
        }
    }
}

/// Server-originated events the UI may observe without owning the
/// transport. Placeholder for now — the Local backend will route
/// threshold crossings / reachability / sampler version changes through
/// here in a follow-up (Leak L2 / L4 in docs/backend-split.md).
enum BackendEvent: Sendable {
    case thresholdCrossed(nodeID: UUID, metric: Metric, tint: ThresholdTint)
    case samplerVersionChanged(nodeID: UUID, version: String)
    case nodeReachabilityChanged(nodeID: UUID, reachable: Bool)
}

/// Single seam the UI goes through for both data-in (observable stores)
/// and actions-out (mutations). `LocalBackend` wraps today's in-process
/// collector/notifier/node stack. `RemoteBackend` (stub) will eventually
/// be a thin HTTP/WebSocket client.
@MainActor
protocol Backend: AnyObject {
    // Observable state surfaces the UI injects via @Environment.
    var servers: ServerStore { get }
    var nodes: NodeStore { get }
    var clientSettings: ClientSettings { get }
    var serverSettings: ServerSettings { get }
    /// Kept on the protocol so server cards can show an "updating…"
    /// indicator without a separate injection. Remote mode returns an
    /// idle stub — the real server pushes the binaries there.
    var samplerUpdater: SamplerUpdateCoordinator { get }

    // Lifecycle.
    func start()
    func stop()

    // Node CRUD. `async throws` so Remote implementations can fail on
    // network errors — Local is effectively infallible today.
    func addNode(_ node: Node) async throws
    func addNodes(_ nodes: [Node]) async throws
    func updateNode(_ node: Node) async throws
    func removeNode(id: UUID) async throws
    func setNodeEnabled(id: UUID, enabled: Bool) async throws
    func setNodeSnooze(id: UUID, until: Date?) async throws
    func setNodeFavorite(id: UUID, favorite: Bool) async throws

    // ServerSettings live here in Local mode and on the server in Remote
    // mode. ClientSettings are always local — mutate them directly on
    // the @Observable.
    func updateServerSettings(_ settings: ServerSettings) async throws

    /// Asks the collector to drop any cached pacer state for `id` and
    /// start fresh on its next tick. Used by the Preferences "Test"
    /// button: after a successful test on a host whose pacer halted on
    /// a permanent error, the user expects polling to resume without
    /// having to disable+re-enable the node. Remote mode forwards this
    /// to the server; Local mode pokes the in-process collector.
    func respawnPacer(id: UUID) async
}

/// SwiftUI environment key for the active backend. Views that mutate
/// state (e.g. ServersPane's add/remove buttons) read this and route
/// calls through `backend.addNode(...)` etc. instead of poking
/// `NodeStore` directly. Default is a lazily-constructed `LocalBackend`
/// for Previews / tests that forget to inject; production App code
/// always supplies the real one from `AppEnvironment`.
@MainActor
private struct BackendEnvironmentKey: @preconcurrency EnvironmentKey {
    static let defaultValue: (any Backend)? = nil
}

extension EnvironmentValues {
    var backend: (any Backend)? {
        get { self[BackendEnvironmentKey.self] }
        set { self[BackendEnvironmentKey.self] = newValue }
    }
}
