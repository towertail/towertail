import Foundation
import Observation

/// Decides when a remote SSH agent is outdated and silently redeploys it.
/// Observable so SwiftUI cards can show an "Updating…" affordance while a
/// push is in flight; main-actor because its state is read by views and
/// mutated by the collector callback.
@Observable
@MainActor
final class AgentUpdateCoordinator {
    private let manifest: AgentManifest?
    /// Node IDs with an in-flight copyBinary task. Observed by the UI to
    /// render the per-card "Updating agent…" affordance.
    private(set) var updatingNodeIDs: Set<UUID> = []
    /// Cooldown table: a failed push won't be retried for the cooldown
    /// window. Prevents a loop of scp failures from hammering the host.
    /// Not @Observable-tracked (private + non-UI-facing).
    @ObservationIgnored
    private var lastAttempt: [UUID: Date] = [:]

    /// Retry cooldown after a failed or successful push — either way we
    /// don't need to try again until the next tick round, and giving the
    /// remote side time to settle avoids spamming on a misconfigured host.
    private let cooldown: TimeInterval = 300

    init(manifest: AgentManifest?) {
        self.manifest = manifest
    }

    func isUpdating(id: UUID) -> Bool {
        updatingNodeIDs.contains(id)
    }

    /// Called from the collector pacer after each successful sample. The
    /// caller is responsible for checking `settings.autoUpdateAgentsEnabled`
    /// and passing `enabled: true` only when the user has opted in — keeping
    /// settings access out of this class avoids crossing two observable
    /// dependency graphs in one call and is simpler to reason about.
    /// No-op for local nodes or when the manifest isn't available.
    func maybeUpdate(node: Node, reportedAgent: String, enabled: Bool) {
        guard enabled else { return }
        guard node.kind == .ssh else { return }
        guard let manifest else { return }
        guard !reportedAgent.isEmpty else { return }
        if reportedAgent == manifest.expectedAgentField { return }
        if updatingNodeIDs.contains(node.id) { return }
        if let last = lastAttempt[node.id],
           Date().timeIntervalSince(last) < cooldown {
            return
        }
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty
        else { return }

        updatingNodeIDs.insert(node.id)
        lastAttempt[node.id] = Date()
        let nodeID = node.id
        Task.detached(priority: .utility) { [weak self] in
            // Detect the remote's OS/arch each time rather than caching —
            // costs one extra ssh round-trip per update, but catches hosts
            // that swap architecture (rare but possible on SBCs migrating
            // from 32-bit armv7 to arm64 firmware).
            do {
                let triple = try await SSHBootstrap.detectTriple(user: user, host: host)
                if let binary = SSHBootstrap.bundledBinary(forTriple: triple) {
                    _ = try await SSHBootstrap.copyBinary(
                        localBinary: binary, user: user, host: host
                    )
                }
            } catch {
                // Swallow — the next successful sample will trigger another
                // attempt after the cooldown window. Surfacing errors here
                // would require a notifier plumbed in, which we don't yet
                // have; the stale-version banner is enough signal.
            }
            await MainActor.run {
                self?.updatingNodeIDs.remove(nodeID)
            }
        }
    }
}
