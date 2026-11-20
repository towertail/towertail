import Foundation
import Observation

/// Owns the Cloud parts of the app: the account and, while the user is
/// signed in, the client and the feed of Cloud host data.
@MainActor
@Observable
final class CloudSession {
    let account: CloudAccount
    private(set) var client: RemoteClient?
    private(set) var feed: CloudFeed?

    @ObservationIgnored private let backend: LocalBackend

    init(account: CloudAccount, backend: LocalBackend) {
        self.account = account
        self.backend = backend
        account.onStatusChange = { [weak self] status in
            self?.accountChanged(status)
        }
    }

    /// A sign-out stops the feed. `Node.cloud` stays, because the samplers
    /// on the hosts keep running.
    private func accountChanged(_ status: CloudAccount.Status) {
        switch status {
        case .signedIn: startFeed()
        case .signedOut: stopFeed()
        case .codeSent, .failed: break
        }
    }

    private func startFeed() {
        guard let token = account.token else { return }
        if let client, client.config.token == token, feed != nil { return }
        stopFeed()
        let client = RemoteClient(config: .init(endpoint: account.endpoint, token: token))
        let feed = CloudFeed(client: client, servers: backend.servers, nodes: backend.nodes)
        self.client = client
        self.feed = feed
        feed.start()
    }

    private func stopFeed() {
        feed?.stop()
        feed = nil
        client = nil
    }
}
