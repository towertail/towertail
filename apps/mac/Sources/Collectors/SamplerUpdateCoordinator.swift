import Foundation
import Observation

/// Decides when a remote SSH sampler is outdated and silently redeploys it.
/// Observable so SwiftUI cards can show an "Updating…" affordance while a
/// push is in flight; main-actor because its state is read by views and
/// mutated by the collector callback.
@Observable
@MainActor
final class SamplerUpdateCoordinator {
    private let manifest: SamplerManifest?
    /// Node IDs with an in-flight copyBinary task. Observed by the UI to
    /// render the per-card "Updating sampler…" affordance.
    private(set) var updatingNodeIDs: Set<UUID> = []
    /// Last update error per node — populated when a push fails, cleared on
    /// the next successful push. Displayed as a tooltip in the Servers pane
    /// so failing auto-updates (marginal SSH links, missing permissions) are
    /// visible without users having to open log files.
    private(set) var lastUpdateError: [UUID: String] = [:]
    /// Cooldown table: a failed push won't be retried for the cooldown
    /// window. Prevents a loop of scp failures from hammering the host.
    /// Not @Observable-tracked (private + non-UI-facing).
    @ObservationIgnored
    private var lastAttempt: [UUID: Date] = [:]
    /// Successful pushes get a much shorter cooldown — we want the very
    /// next tick to pick up the new version so the UI doesn't show "stale"
    /// for 5 full minutes. Failures use the longer cooldown so marginal
    /// hosts aren't hammered.
    private let cooldownSuccess: TimeInterval = 15
    private let cooldownFailure: TimeInterval = 300

    init(manifest: SamplerManifest?) {
        self.manifest = manifest
    }

    func isUpdating(id: UUID) -> Bool {
        updatingNodeIDs.contains(id)
    }

    /// Called from the collector pacer after each successful sample. The
    /// caller is responsible for checking `settings.autoUpdateSamplersEnabled`
    /// and passing `enabled: true` only when the user has opted in — keeping
    /// settings access out of this class avoids crossing two observable
    /// dependency graphs in one call and is simpler to reason about.
    /// No-op for local nodes or when the manifest isn't available.
    func maybeUpdate(node: Node, reportedSampler: String, enabled: Bool) {
        guard enabled else { return }
        guard node.kind == .ssh else { return }
        guard let manifest else { return }
        guard !reportedSampler.isEmpty else { return }
        if reportedSampler == manifest.expectedSamplerField {
            // Already current: clear any stale error banner from a prior
            // failed attempt so the UI doesn't lie to the user.
            lastUpdateError.removeValue(forKey: node.id)
            return
        }
        if updatingNodeIDs.contains(node.id) { return }
        // Per-node cooldown: shorter after success so the next tick picks
        // up the fresh version promptly.
        if let last = lastAttempt[node.id] {
            let hadError = lastUpdateError[node.id] != nil
            let cd = hadError ? cooldownFailure : cooldownSuccess
            if Date().timeIntervalSince(last) < cd { return }
        }
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty
        else { return }
        _ = (user, host)

        updatingNodeIDs.insert(node.id)
        lastAttempt[node.id] = Date()
        let nodeID = node.id
        let nodeHostname = node.displayName
        let fromVersion = reportedSampler
        let expected = manifest.expectedSamplerField
        let nodeCopy = node
        Logger.shared.info(
            "auto-update: push starting", hostID: nodeID, host: nodeHostname,
            kv: ["from": fromVersion, "to": expected]
        )
        Task.detached(priority: .utility) { [weak self] in
            let start = Date()
            var triple: String?
            var failure: Error?
            do {
                let t = try await SSHBootstrap.pushUpdate(node: nodeCopy)
                triple = t
            } catch {
                failure = error
            }
            let elapsed = Date().timeIntervalSince(start)
            await MainActor.run {
                self?.updatingNodeIDs.remove(nodeID)
                if let failure {
                    let msg = SamplerInvokeError.shortDescription(for: failure)
                    self?.lastUpdateError[nodeID] = msg
                    Logger.shared.error(
                        "auto-update: push failed", hostID: nodeID, host: nodeHostname,
                        kv: [
                            "triple": triple ?? "-",
                            "elapsed_s": String(format: "%.1f", elapsed),
                            "error": msg,
                        ]
                    )
                } else {
                    self?.lastUpdateError.removeValue(forKey: nodeID)
                    Logger.shared.info(
                        "auto-update: push succeeded", hostID: nodeID, host: nodeHostname,
                        kv: [
                            "triple": triple ?? "-",
                            "elapsed_s": String(format: "%.1f", elapsed),
                            "to": expected,
                        ]
                    )
                }
            }
        }
    }
}
