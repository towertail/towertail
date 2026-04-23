import Foundation
import SwiftUI

/// Thin shell around a `Backend` implementation. Exposes accessors so
/// existing `@Environment(...)` injection sites don't have to go through
/// `env.backend.servers` at every callsite. The backend owns all
/// construction and lifecycle.
@MainActor
final class AppEnvironment {
    let backend: any Backend

    /// Flipping from .local to .remote here is all that should be
    /// required to swap implementations — every mutation goes through
    /// `backend`, and observable state is read via the forwarding
    /// accessors below.
    static let defaultMode: BackendMode = .local

    var store: ServerStore { backend.servers }
    var nodeStore: NodeStore { backend.nodes }
    var clientSettings: ClientSettings { backend.clientSettings }
    var serverSettings: ServerSettings { backend.serverSettings }
    var samplerUpdater: SamplerUpdateCoordinator { backend.samplerUpdater }

    init(mode: BackendMode = .local) {
        switch mode {
        case .local:
            self.backend = LocalBackend()
        case .remote:
            self.backend = RemoteBackend()
        }
    }

    func start() { backend.start() }
    func stop()  { backend.stop() }
}
