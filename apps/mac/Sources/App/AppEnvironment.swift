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
    /// The Cloud account and data path. Nil in remote mode.
    let cloud: CloudSession?

    /// Remote mode is a developer override. It needs the env vars
    /// `TOWERTAIL_REMOTE_URL` and `TOWERTAIL_REMOTE_TOKEN` and a debug
    /// build. Else the app runs in .local mode with the Cloud account.
    static var defaultMode: BackendMode {
        #if DEBUG
        let env = ProcessInfo.processInfo.environment
        if env["TOWERTAIL_REMOTE_URL"] != nil, env["TOWERTAIL_REMOTE_TOKEN"] != nil {
            return .remote
        }
        #endif
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
            let local = LocalBackend()
            self.backend = local
            self.cloud = Self.makeCloud(backend: local)
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
                self.cloud = nil
                return
            }
            self.backend = RemoteBackend(endpoint: url, token: token)
            self.cloud = nil
        }
    }

    private static func makeCloud(backend: LocalBackend) -> CloudSession {
        let endpoint = CloudAccount.configuredEndpoint()
        let account = CloudAccount(
            endpoint: endpoint,
            keychain: CloudKeychain(),
            client: CloudAuthClient(endpoint: endpoint)
        )
        return CloudSession(account: account, backend: backend)
    }

    func start() {
        backend.start()
        updater.start()
        if let account = cloud?.account {
            Task { await account.restore() }
        }
        let settings = serverSettings
        store.startStaleWatchdog { settings.pollingInterval(for: $0) }
    }
    func stop() {
        store.stopStaleWatchdog()
        backend.stop()
    }
}
