import Foundation
import Observation

/// Moves hosts from SSH polling to a sampler that reports to Cloud. It
/// opens SSH only to install, remove or check the sampler.
@MainActor
@Observable
final class CloudInstaller {
    typealias Connect = @Sendable (Node) async throws -> any SamplerSession
    typealias Upload = @Sendable (any SamplerSession, URL) async throws -> String
    typealias BinaryLookup = @Sendable (String) -> URL?
    /// The sampler version of the first sample that came after a date. Nil when there is none.
    typealias FirstSample = @MainActor (UUID, Date) -> String?

    static let sampleWait = Duration.seconds(90)
    static let maxParallel = 4

    /// Opens SSH with the host key prompt of the app.
    static let sshConnect: Connect = { node in
        let id = node.id
        let client = try await SSHConnectionFactory.connect(
            node: node,
            hostKeyPrompt: HostKeyTrustPrompter.sharedPromptAdapter,
            onTrust: { fp in Task { @MainActor in HostKeyTrustPersister.persist(nodeId: id, fingerprint: fp) } }
        )
        return CitadelSession(client: client)
    }

    static let sshUpload: Upload = { session, binary in
        guard let session = session as? CitadelSession else {
            throw LifecycleError.unsupportedHost("no SSH session")
        }
        return try await SSHBootstrap.copyBinary(client: session.client, localBinary: binary)
    }

    private(set) var rows: [UUID: [InstallStep: InstallStepState]] = [:]
    /// One line for each host, when the run ends.
    private(set) var results: [UUID: String] = [:]
    private(set) var notes: [UUID: String] = [:]

    @ObservationIgnored private let client: @MainActor () -> RemoteClient?
    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let settings: ServerSettings
    @ObservationIgnored private let planName: @MainActor () -> String?
    @ObservationIgnored private let firstSample: FirstSample
    @ObservationIgnored private let connect: Connect
    @ObservationIgnored private let upload: Upload
    @ObservationIgnored private let binary: BinaryLookup
    @ObservationIgnored private let clock: any Clock<Duration>

    init(
        client: @escaping @MainActor () -> RemoteClient?,
        nodes: NodeStore,
        settings: ServerSettings,
        planName: @escaping @MainActor () -> String? = { nil },
        firstSample: @escaping FirstSample,
        connect: @escaping Connect = CloudInstaller.sshConnect,
        upload: @escaping Upload = CloudInstaller.sshUpload,
        binary: @escaping BinaryLookup = { SSHBootstrap.bundledBinary(forTriple: $0) },
        clock: any Clock<Duration> = ContinuousClock()
    ) {
        self.client = client
        self.nodes = nodes
        self.settings = settings
        self.planName = planName
        self.firstSample = firstSample
        self.connect = connect
        self.upload = upload
        self.binary = binary
        self.clock = clock
    }

    convenience init(session: CloudSession, servers: ServerStore, nodes: NodeStore, settings: ServerSettings) {
        self.init(
            client: { session.client },
            nodes: nodes,
            settings: settings,
            planName: {
                if case .signedIn(let me) = session.account.status { return me.org.plan }
                return nil
            },
            firstSample: { id, since in
                guard let vm = servers.serverVMs.first(where: { $0.id == id }),
                      let at = vm.lastIngestAt, at > since else { return nil }
                return vm.samplerVersion
            }
        )
    }

    func run(nodes list: [Node]) async {
        for node in list {
            rows[node.id] = Dictionary(uniqueKeysWithValues: InstallStep.allCases.map { ($0, InstallStepState.waiting) })
            results[node.id] = nil
            notes[node.id] = nil
        }
        await withTaskGroup(of: Void.self) { group in
            var queue = list.makeIterator()
            var active = 0
            while active < Self.maxParallel, let node = queue.next() {
                group.addTask { await self.install(node) }
                active += 1
            }
            while await group.next() != nil {
                if let node = queue.next() {
                    group.addTask { await self.install(node) }
                }
            }
        }
    }

    /// Stops 24/7 monitoring. Cloud orders the sampler to exit at its next push.
    func turnOff(node: Node) async throws {
        guard let client = client() else { throw LifecycleError.notSignedIn }
        var remote = node.toRemote(global: settings.thresholds)
        remote.enabled = false
        _ = try await client.updateNode(remote)
        clearCloud(node.id)
    }

    /// Reads the sampler status over SSH and saves it on the host link.
    func status(node: Node) async throws -> SamplerStatus {
        let status = try await withLifecycle(node) { try await $0.status() }
        updateCloud(node.id) { $0.lastStatus = status }
        return status
    }

    /// Starts a stopped sampler with its saved config.
    func restart(node: Node) async throws -> String {
        try await withLifecycle(node) { try await $0.restart() }
    }

    private func withLifecycle<T>(_ node: Node, _ body: (SamplerLifecycle) async throws -> T) async throws -> T {
        let session = try await connect(node)
        let upload = self.upload
        let lifecycle = SamplerLifecycle(session: session, enroller: nil, upload: { try await upload(session, $0) })
        do {
            let value = try await body(lifecycle)
            await session.close()
            return value
        } catch {
            await session.close()
            throw error
        }
    }

    /// Removes the sampler from the host (when SSH answers) and the host from Cloud.
    func remove(node: Node) async throws {
        guard let client = client() else { throw LifecycleError.notSignedIn }
        if node.kind == .ssh, let session = try? await connect(node) {
            let lifecycle = SamplerLifecycle(session: session, enroller: nil, upload: { _ in "" })
            try? await lifecycle.remove()
            await session.close()
        }
        try await client.deleteNode(id: node.id)
        clearCloud(node.id)
    }

    // MARK: one host

    private func install(_ node: Node) async {
        let id = node.id
        guard let client = client() else {
            fail(id, .copy, LifecycleError.notSignedIn)
            return
        }
        set(id, .copy, .running)
        let session: any SamplerSession
        do {
            session = try await connect(node)
        } catch {
            fail(id, .copy, error)
            return
        }
        let upload = self.upload
        let lifecycle = SamplerLifecycle(session: session, enroller: client, upload: { try await upload(session, $0) })
        await install(node, lifecycle: lifecycle, client: client)
        await session.close()
    }

    private func install(_ node: Node, lifecycle: SamplerLifecycle, client: RemoteClient) async {
        let id = node.id
        do {
            let triple = try await lifecycle.triple()
            guard let file = binary(triple) else {
                throw LifecycleError.unsupportedHost("no bundled sampler for \(triple)")
            }
            _ = try await lifecycle.install(binary: file)
            set(id, .copy, .done(triple))
        } catch {
            fail(id, .copy, error)
            return
        }

        set(id, .register, .running)
        let enrollment: Enrollment
        do {
            enrollment = try await lifecycle.enroll(node: node)
            set(id, .register, .done(nil))
        } catch LifecycleError.planFull(let max) {
            let text = planFullText(max)
            set(id, .register, .failed(text))
            results[id] = text
            notes[id] = "Live view over SSH still works"
            return
        } catch {
            fail(id, .register, error)
            return
        }

        set(id, .start, .running)
        let since = Date()
        do {
            let line = try await lifecycle.start(enrollment: enrollment)
            set(id, .start, .done(line))
        } catch {
            fail(id, .start, error)
            return
        }
        // The feed drops samples of a host with no link, so the link comes now.
        updateCloud(id) { _ in }

        set(id, .reboot, .running)
        do {
            let mode = try await lifecycle.enableReboot()
            set(id, .reboot, .done(mode))
            updateCloud(id) { $0.reboot = mode }
        } catch {
            // The sampler runs until the next reboot, so the install goes on.
            set(id, .reboot, .failed(error.localizedDescription))
        }

        set(id, .sample, .running)
        var waited = Duration.zero
        let step = Duration.seconds(1)
        while true {
            if let version = firstSample(id, since) {
                set(id, .sample, .done(version))
                results[id] = "Reporting to Cloud"
                updateCloud(id) { $0.samplerVersion = version }
                return
            }
            if waited >= Self.sampleWait { break }
            try? await clock.sleep(for: step)
            waited += step
        }
        let text = "No sample yet. Check the sampler log."
        set(id, .sample, .failed(text))
        results[id] = text
    }

    private func planFullText(_ max: Int?) -> String {
        let name = planName().map { $0.prefix(1).uppercased() + $0.dropFirst() + " plan" } ?? "Plan"
        guard let max else { return "\(name) is full" }
        return "\(name) is full (\(max) of \(max))"
    }

    private func set(_ id: UUID, _ step: InstallStep, _ state: InstallStepState) {
        rows[id, default: [:]][step] = state
    }

    private func fail(_ id: UUID, _ step: InstallStep, _ error: Error) {
        let text = error.localizedDescription
        set(id, step, .failed(text))
        results[id] = text
    }

    private func updateCloud(_ id: UUID, _ edit: (inout CloudLink) -> Void) {
        guard var node = nodes.node(withId: id) else { return }
        var link = node.cloud ?? CloudLink(enrolledAt: Date())
        edit(&link)
        node.cloud = link
        nodes.update(node)
    }

    private func clearCloud(_ id: UUID) {
        guard var node = nodes.node(withId: id) else { return }
        node.cloud = nil
        nodes.update(node)
    }
}
