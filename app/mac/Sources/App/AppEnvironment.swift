import Foundation
import SwiftUI

/// Thin shell around a `Backend` implementation. Exposes accessors so
/// existing `@Environment(...)` injection sites don't have to go through
/// `env.backend.servers` at every callsite. The backend owns all
/// construction and lifecycle.
@MainActor
final class AppEnvironment {
    let backend: any Backend
    let updater = Updater()

    /// Remote mode is selected by env vars `TOWERTAIL_REMOTE_URL` +
    /// `TOWERTAIL_REMOTE_TOKEN` at launch. Absent both, we stay in
    /// .local. A proper mode toggle (Preferences → Account) lands in
    /// Phase L along with keychain-stored credentials.
    static var defaultMode: BackendMode {
        let env = ProcessInfo.processInfo.environment
        if env["TOWERTAIL_REMOTE_URL"] != nil, env["TOWERTAIL_REMOTE_TOKEN"] != nil {
            return .remote
        }
        return .local
    }

    var store: ServerStore { backend.servers }
    var nodeStore: NodeStore { backend.nodes }
    var clientSettings: ClientSettings { backend.clientSettings }
    var serverSettings: ServerSettings { backend.serverSettings }
    var samplerUpdater: SamplerUpdateCoordinator { backend.samplerUpdater }

    init(mode: BackendMode = AppEnvironment.defaultMode) {
        switch mode {
        case .local:
            self.backend = LocalBackend()
        case .remote:
            let env = ProcessInfo.processInfo.environment
            guard
                let raw = env["TOWERTAIL_REMOTE_URL"],
                let url = URL(string: raw),
                let token = env["TOWERTAIL_REMOTE_TOKEN"]
            else {
                Logger.shared.warn(
                    "remote: env missing — falling back to local",
                    category: "lifecycle"
                )
                self.backend = LocalBackend()
                return
            }
            self.backend = RemoteBackend(endpoint: url, token: token)
        }
    }

    func start() {
        backend.start()
        updater.start()
    }
    func stop()  { backend.stop() }
}
