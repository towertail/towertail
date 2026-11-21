import Foundation
import Observation

/// Leaves Cloud: moves the hosts back to SSH, then signs out or deletes
/// the Cloud account.
@MainActor
@Observable
final class CloudExit {
    enum Phase: Equatable {
        case idle, moving, deleting, done
        case failed(String)
    }

    private(set) var phase: Phase = .idle
    /// The error text of each host that could not move back.
    private(set) var failures: [UUID: String] = [:]
    private(set) var deletion: CloudAccountDeletion?

    @ObservationIgnored private let account: CloudAccount
    @ObservationIgnored private let installer: CloudInstaller
    @ObservationIgnored private let nodes: NodeStore

    init(account: CloudAccount, installer: CloudInstaller, nodes: NodeStore) {
        self.account = account
        self.installer = installer
        self.nodes = nodes
    }

    /// SSH hosts that report to Cloud.
    var linkedHosts: [Node] { nodes.nodes.filter { $0.kind == .ssh && $0.cloud != nil } }

    /// Hosts that only Cloud knows. They leave this Mac when the user signs out.
    var cloudOnlyHosts: [Node] { nodes.nodes.filter { $0.kind == .cloud } }

    var isBusy: Bool { phase == .moving || phase == .deleting }

    func reset() {
        phase = .idle
        failures = [:]
    }

    /// Moves the hosts back, then signs out. A failure keeps the sign-in.
    func leave() async {
        guard await moveHosts() else { return }
        account.signOut()
        phase = .done
    }

    func loadDeletion() async {
        do {
            guard let found = try await account.deletion() else {
                phase = .failed("This Cloud server cannot delete accounts yet.")
                return
            }
            deletion = found
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    /// Moves the hosts back, then deletes the account. The hosts move first,
    /// because the samplers need the token to leave.
    func deleteAccount(email: String) async {
        phase = .moving
        await loadDeletion()
        if case .failed = phase { return }
        guard let deletion else { return }
        if deletion.blocked {
            let reasons = deletion.orgs.compactMap { org in org.reason.map { "\(org.name): \($0)" } }
            phase = .failed(reasons.isEmpty ? "Cloud blocks the delete." : reasons.joined(separator: " "))
            return
        }
        guard await moveHosts() else { return }
        phase = .deleting
        do {
            try await account.deleteAccount(email: email)
            phase = .done
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    private func moveHosts() async -> Bool {
        phase = .moving
        let hosts = linkedHosts
        failures = await installer.leave(nodes: hosts)
        guard failures.isEmpty else {
            let names = hosts.filter { failures[$0.id] != nil }.map(\.displayName).sorted()
            phase = .failed("These hosts did not move back: \(names.joined(separator: ", ")).")
            return false
        }
        return true
    }
}
