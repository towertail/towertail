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
    /// Loads the history of Cloud hosts over REST while signed in.
    @ObservationIgnored private(set) var history: RemoteHistoryLoader?
    /// The rules of the cloud, for the hosts of the org.
    private(set) var rules: CloudRules?

    /// Reports what the user looks at. It outlives the feed, so a
    /// sign-in while the popover is open sends the view.
    @ObservationIgnored private(set) lazy var viewLease = ViewLeaseController(send: { [weak self] view in
        self?.feed?.send(view)
    })

    @ObservationIgnored private let servers: ServerStore
    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let released: ReleasedHosts
    @ObservationIgnored private let notifier: ThresholdNotifier?
    @ObservationIgnored private let rulesCache: CloudRulesCache?

    /// The name of this Mac, for the origin of the rules that it sets.
    static var deviceName: String { Host.current().localizedName ?? ProcessInfo.processInfo.hostName }

    var isSignedIn: Bool {
        if case .signedIn = account.status { return true }
        return false
    }

    convenience init(account: CloudAccount, backend: LocalBackend) {
        self.init(account: account, servers: backend.servers, nodes: backend.nodes, notifier: backend.notifier,
                  rulesCache: .standard())
    }

    init(account: CloudAccount, servers: ServerStore, nodes: NodeStore, released: ReleasedHosts = ReleasedHosts(), notifier: ThresholdNotifier? = nil, rulesCache: CloudRulesCache? = nil) {
        self.notifier = notifier
        self.rulesCache = rulesCache
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
        let feed = CloudFeed(client: client, servers: servers, nodes: nodes, viewLease: viewLease, released: released, notifier: notifier)
        self.client = client
        self.feed = feed
        history = RemoteHistoryLoader(cache: CloudHistoryCache(client: client), store: servers)
        let rules = CloudRules(client: client, device: Self.deviceName, cache: rulesCache)
        self.rules = rules
        feed.rules = rules
        NotificationTapRouter.shared.setActionHandler { [weak self] alertId, action in
            await self?.perform(action, alertId: alertId)
        }
        feed.start()
        Task { await rules.refresh() }
    }

    /// Runs an action button of a cloud notification.
    func perform(_ action: CloudAlertAction, alertId: UUID) async {
        guard let client else { return }
        do {
            switch action {
            case .ack: _ = try await client.ackAlert(id: alertId)
            case .snooze(let until): _ = try await client.snoozeAlert(id: alertId, until: until)
            }
        } catch {
            let name: String
            switch action {
            case .ack: name = "Ack"
            case .snooze: name = "Snooze"
            }
            notifier?.postCloudFailure("\(name) failed: \(error.localizedDescription)")
        }
    }

    private func stopFeed() {
        feed?.stop()
        feed = nil
        rules?.stop()
        if rules != nil { rulesCache?.clear() }
        rules = nil
        history = nil
        client = nil
    }
}

extension CloudSession: CloudRulesSource {
    func effective(for nodeID: UUID) -> RemoteEffectiveRules? { rules?.effective(for: nodeID) }
}
