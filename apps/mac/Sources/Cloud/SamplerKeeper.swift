import Foundation
import Observation

/// Restarts a stopped sampler on a Cloud host that Cloud marks as alerted,
/// and runs the update that Cloud orders.
@MainActor
final class SamplerKeeper {
    static let revisit: TimeInterval = 600

    private let alerted: @MainActor () -> Set<UUID>
    private let nodes: NodeStore
    private let manifest: SamplerManifest?
    private let connect: CloudInstaller.Connect
    private let upload: CloudInstaller.Upload
    private let binary: CloudInstaller.BinaryLookup
    private let now: () -> Date
    private var lastVisit: [UUID: Date] = [:]

    init(
        alerted: @escaping @MainActor () -> Set<UUID>,
        nodes: NodeStore,
        manifest: SamplerManifest?,
        connect: @escaping CloudInstaller.Connect = CloudInstaller.sshConnect,
        upload: @escaping CloudInstaller.Upload = CloudInstaller.sshUpload,
        binary: @escaping CloudInstaller.BinaryLookup = { SSHBootstrap.bundledBinary(forTriple: $0) },
        now: @escaping () -> Date = Date.init
    ) {
        self.alerted = alerted
        self.nodes = nodes
        self.manifest = manifest
        self.connect = connect
        self.upload = upload
        self.binary = binary
        self.now = now
    }

    convenience init(session: CloudSession, nodes: NodeStore, manifest: SamplerManifest?) {
        self.init(alerted: { session.feed?.alerted ?? [] }, nodes: nodes, manifest: manifest)
    }

    func start() {
        let current = withObservationTracking {
            alerted()
        } onChange: { [weak self] in
            Task { @MainActor in self?.start() }
        }
        Task { await handle(current) }
    }

    /// One try for each host in each 10 minutes.
    func handle(_ alerted: Set<UUID>) async {
        for id in alerted {
            guard let node = nodes.node(withId: id), node.cloud != nil, node.kind == .ssh,
                  node.sshUser?.isEmpty == false, node.sshHost?.isEmpty == false else { continue }
            if let last = lastVisit[id], now().timeIntervalSince(last) < Self.revisit { continue }
            lastVisit[id] = now()
            await visit(node)
        }
    }

    private func visit(_ node: Node) async {
        let session: any SamplerSession
        do {
            session = try await connect(node)
        } catch {
            Logger.shared.warn("keeper: SSH failed: \(error.localizedDescription)", category: "cloud", hostID: node.id, host: node.displayName)
            return
        }
        let upload = self.upload
        let lifecycle = SamplerLifecycle(session: session, enroller: nil, upload: { try await upload(session, $0) })
        do {
            var status = try await lifecycle.status()
            if !status.running {
                _ = try await lifecycle.restart()
                Logger.shared.info("restarted the sampler on \(node.displayName)", category: "cloud", hostID: node.id, host: node.displayName)
                status = (try? await lifecycle.status()) ?? status
            } else if let error = status.lastError, !error.isEmpty {
                Logger.shared.warn("sampler cannot reach Cloud: \(error)", category: "cloud", hostID: node.id, host: node.displayName)
            }
            save(status, for: node.id)
            try await applyUpdate(status, lifecycle)
        } catch {
            Logger.shared.warn("keeper: \(error.localizedDescription)", category: "cloud", hostID: node.id, host: node.displayName)
        }
        await session.close()
    }

    /// Runs the update when Cloud asks for the version that the app bundles.
    private func applyUpdate(_ status: SamplerStatus, _ lifecycle: SamplerLifecycle) async throws {
        guard let want = status.update, !want.isEmpty, let manifest else { return }
        let have = manifest.expectedSamplerField
        guard want == have || have.hasPrefix(want + "+"), status.version != have else { return }
        let triple = try await lifecycle.triple()
        guard let file = binary(triple) else { return }
        try await lifecycle.update(binary: file)
    }

    private func save(_ status: SamplerStatus, for id: UUID) {
        guard var node = nodes.node(withId: id), var link = node.cloud else { return }
        link.lastStatus = status
        node.cloud = link
        nodes.update(node)
    }
}
