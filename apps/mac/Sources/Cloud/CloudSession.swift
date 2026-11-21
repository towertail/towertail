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

    /// Reports what the user looks at. It outlives the feed, so a
    /// sign-in while the popover is open sends the view.
    @ObservationIgnored private(set) lazy var viewLease = ViewLeaseController(send: { [weak self] view in
        self?.feed?.send(view)
    })

    @ObservationIgnored private let servers: ServerStore
    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let released: ReleasedHosts

    var isSignedIn: Bool {
        if case .signedIn = account.status { return true }
        return false
    }

    convenience init(account: CloudAccount, backend: LocalBackend) {
        self.init(account: account, servers: backend.servers, nodes: backend.nodes)
    }

    init(account: CloudAccount, servers: ServerStore, nodes: NodeStore, released: ReleasedHosts = ReleasedHosts()) {
        self.account = account
        self.servers = servers
        self.nodes = nodes
        self.released = released
        account.onStatusChange = { [weak self] status in
            self?.accountChanged(status)
        }
    }

    /// A sign-out stops the feed and releases the hosts. SSH polling
    /// starts again, and Cloud-only hosts leave this Mac.
    private func accountChanged(_ status: CloudAccount.Status) {
        switch status {
        case .signedIn: startFeed()
        case .signedOut:
            stopFeed()
            releaseHosts()
        case .codeSent, .failed: break
        }
    }

    private func releaseHosts() {
        var freed: [UUID] = []
        for node in nodes.nodes where node.isCloud {
            if node.kind == .cloud {
                nodes.remove(id: node.id)
            } else {
                var local = node
                local.cloud = nil
                nodes.update(local)
                freed.append(node.id)
            }
        }
        released.add(freed)
    }

    private func startFeed() {
        guard let token = account.token else { return }
        if let client, client.config.token == token, feed != nil { return }
        stopFeed()
        let client = RemoteClient(config: .init(endpoint: account.endpoint, token: token))
        let feed = CloudFeed(client: client, servers: servers, nodes: nodes, viewLease: viewLease, released: released)
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
