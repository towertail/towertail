import Foundation
import Observation

/// Moves SSH hosts that exist already to Cloud. It drives the
/// "Move hosts to Cloud" sheet of the Settings window.
@MainActor
@Observable
final class CloudMove {
    enum Phase { case pick, install }

    /// Set after the sheet opened by itself one time.
    static let offeredKey = "cloudMoveOffered"

    var isPresented = false
    private(set) var phase: Phase = .pick
    /// The SSH hosts with no Cloud link when the sheet opened.
    private(set) var hosts: [Node] = []
    private(set) var picks = CloudPicks(room: nil)

    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored let installer: CloudInstaller
    @ObservationIgnored private let signedIn: @MainActor () -> Bool
    @ObservationIgnored private let billing: @MainActor () -> CloudBilling?
    @ObservationIgnored private let defaults: UserDefaults

    init(
        nodes: NodeStore,
        installer: CloudInstaller,
        signedIn: @escaping @MainActor () -> Bool,
        billing: @escaping @MainActor () -> CloudBilling?,
        defaults: UserDefaults = .standard
    ) {
        self.nodes = nodes
        self.installer = installer
        self.signedIn = signedIn
        self.billing = billing
        self.defaults = defaults
    }

    convenience init(session: CloudSession, installer: CloudInstaller, nodes: NodeStore) {
        self.init(
            nodes: nodes,
            installer: installer,
            signedIn: { session.isSignedIn },
            billing: { session.account.billing }
        )
    }

    var sshHosts: [Node] { nodes.nodes.filter { $0.kind == .ssh && $0.cloud == nil } }

    var canMove: Bool { signedIn() && !sshHosts.isEmpty }

    var isFull: Bool { billing()?.room == 0 }

    /// The text of the Cloud card. Nil when the plan is not known.
    var summary: String? {
        guard signedIn(), let b = billing() else { return nil }
        let ssh = sshHosts.count
        return "\(b.nodes) of \(b.maxNodes ?? b.plan.maxNodes) Cloud hosts used. \(ssh) \(ssh == 1 ? "host" : "hosts") on SSH."
    }

    var picked: [Node] { hosts.filter { picks.ids.contains($0.id) } }

    var notPicked: Int { hosts.count - picked.count }

    var note: String {
        CloudPicks.note(room: picks.room, planLabel: installer.planLabel, used: (billing()?.nodes ?? 0) > 0)
    }

    func open() async {
        let list = sshHosts
        let room = await installer.availableRoom()
        hosts = list
        picks = CloudPicks(hosts: list, room: room)
        phase = .pick
        isPresented = true
    }

    /// Opens the sheet one time after sign-in, when SSH hosts exist and the plan has room.
    func offerOnce() async {
        guard !defaults.bool(forKey: Self.offeredKey), canMove else { return }
        guard await installer.availableRoom() != 0 else { return }
        defaults.set(true, forKey: Self.offeredKey)
        await open()
    }

    func toggle(_ id: UUID) { picks.toggle(id) }

    func clear() { picks.clear() }

    func start() async {
        let list = picked
        guard phase == .pick, !list.isEmpty else { return }
        phase = .install
        await installer.run(nodes: list)
        _ = await installer.availableRoom()
    }

    func close() { isPresented = false }
}
