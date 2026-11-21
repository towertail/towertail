import AppKit
import Citadel
import Foundation
import NIO
import Observation

struct WelcomeState {
    /// The window shows at the first launch: no SSH host yet and not closed before.
    @MainActor
    static func shouldShow(nodes: [Node], settings: ClientSettings) -> Bool {
        !settings.welcomeDone && !nodes.contains { $0.kind == .ssh }
    }
}

/// The five steps of the welcome window and the state between them.
@MainActor
@Observable
final class WelcomeFlow {
    enum Step: Int, CaseIterable { case welcome, servers, cloud, signIn, install }

    /// The result of the quick SSH test of one candidate.
    typealias Probe = @Sendable (ServerCandidate) async -> (ServerCandidate.TestResult, os: String?)
    /// The copy of the sampler and the first `--once` run.
    typealias Verify = @Sendable (Node) async throws -> Void

    static let maxParallel = 4

    private(set) var step: Step = .welcome
    private(set) var candidates: [ServerCandidate] = []
    var selected: Set<ServerCandidate.ID> = []
    var email: String
    private(set) var tailscaleCount: Int?
    private(set) var sshConfigCount: Int?
    private(set) var tailscaleError: String?
    private(set) var adding = false
    private(set) var added: [Node] = []
    private(set) var addFailures: [UUID: String] = [:]
    private(set) var installStarted = false
    /// The hosts that the plan can still take. Nil means no cap or not read yet.
    private(set) var room: Int?
    private(set) var roomLoaded = false
    /// The hosts that go to Cloud.
    private(set) var cloudPicks: Set<UUID> = []

    /// The window closes through this. The view sets it.
    @ObservationIgnored var close: () -> Void = {}

    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let settings: ClientSettings
    @ObservationIgnored private let cloud: CloudSession?
    @ObservationIgnored let installer: CloudInstaller?
    @ObservationIgnored private let discovery: ServerDiscovery
    @ObservationIgnored private let probe: Probe
    @ObservationIgnored private let verifyHost: Verify
    @ObservationIgnored private let openURL: (URL) -> Void
    @ObservationIgnored private var prebuilt: [ServerCandidate.ID: Node] = [:]

    init(
        nodes: NodeStore,
        settings: ClientSettings,
        cloud: CloudSession?,
        installer: CloudInstaller?,
        discovery: ServerDiscovery = DefaultServerDiscovery(),
        probe: @escaping Probe = WelcomeFlow.sshProbe,
        verify: @escaping Verify = WelcomeFlow.sshVerify,
        openURL: @escaping (URL) -> Void = { NSWorkspace.shared.open($0) }
    ) {
        self.nodes = nodes
        self.settings = settings
        self.cloud = cloud
        self.installer = installer
        self.discovery = discovery
        self.probe = probe
        self.verifyHost = verify
        self.openURL = openURL
        self.email = UserDefaults.standard.string(forKey: CloudAccount.emailKey) ?? ""
    }

    // MARK: steps

    func next() {
        switch step {
        case .welcome: step = .servers
        case .servers: step = .cloud
        case .cloud: step = cloud?.isSignedIn == true ? .install : .signIn
        case .signIn: step = .install
        case .install: finish()
        }
    }

    func back() {
        guard step != .welcome, step != .install, let previous = Step(rawValue: step.rawValue - 1) else { return }
        step = previous
    }

    func skip() { finish() }

    func finish() {
        markDone()
        close()
    }

    func markDone() {
        guard !settings.welcomeDone else { return }
        settings.welcomeDone = true
        settings.persist()
    }

    // MARK: servers

    func loadSources() async {
        do {
            let found = try await discovery.tailscale()
            tailscaleCount = found.count
            merge(found)
        } catch {
            tailscaleCount = 0
            tailscaleError = error.localizedDescription
        }
        if let found = try? discovery.sshConfig() {
            sshConfigCount = found.count
            merge(found)
        } else {
            sshConfigCount = 0
        }
        await testAll()
    }

    func addByHand(_ node: Node) {
        let candidate = ServerCandidate(
            id: "hand:\(node.id.uuidString)",
            name: node.displayName,
            address: node.sshHost ?? "",
            user: node.sshUser ?? NSUserName(),
            port: node.sshPort,
            source: .hand
        )
        prebuilt[candidate.id] = node
        candidates.append(candidate)
        selected.insert(candidate.id)
        Task { await test([candidate.id]) }
    }

    /// Selects all hosts of a source, or none when all are selected.
    func toggleAll(from source: ServerCandidate.Source) {
        let ids = Set(candidates.filter { $0.source == source }.map(\.id))
        if ids.isSubset(of: selected) { selected.subtract(ids) } else { selected.formUnion(ids) }
    }

    /// Hosts that answer to the key. A host in two sources shows once.
    var keyWorks: Int { candidates.filter { $0.test.works }.count }

    func testAll() async { await test(candidates.map(\.id)) }

    private func test(_ ids: [ServerCandidate.ID]) async {
        let probe = self.probe
        await withTaskGroup(of: (ServerCandidate.ID, ServerCandidate.TestResult, String?).self) { group in
            var queue = ids.compactMap { id in candidates.first { $0.id == id } }.makeIterator()
            func launch() {
                guard let candidate = queue.next() else { return }
                group.addTask {
                    let r = await probe(candidate)
                    return (candidate.id, r.0, r.os)
                }
            }
            for _ in 0..<Self.maxParallel { launch() }
            while let (id, result, os) = await group.next() {
                if let i = candidates.firstIndex(where: { $0.id == id }) {
                    candidates[i].test = result
                    if candidates[i].os == nil { candidates[i].os = os }
                }
                launch()
            }
        }
    }

    /// Makes the hosts, then copies the sampler and runs it once on each.
    func addSelected() async {
        let chosen = candidates.filter { selected.contains($0.id) }
        guard !chosen.isEmpty, !adding else { return }
        adding = true
        let made = chosen.map { prebuilt[$0.id] ?? $0.node() }
        nodes.addMany(made)
        added.append(contentsOf: made)
        let verifyHost = self.verifyHost
        await withTaskGroup(of: (UUID, String?).self) { group in
            for node in made {
                group.addTask {
                    do {
                        try await verifyHost(node)
                        return (node.id, nil)
                    } catch {
                        return (node.id, error.localizedDescription)
                    }
                }
            }
            for await (id, failure) in group {
                if let failure { addFailures[id] = failure } else { nodes.markConnected(id: id) }
            }
        }
        candidates.removeAll { selected.contains($0.id) }
        selected = []
        adding = false
    }

    private func merge(_ found: [ServerCandidate]) {
        for candidate in found where !candidates.contains(where: { $0.address == candidate.address && $0.user == candidate.user }) {
            candidates.append(candidate)
        }
    }

    // MARK: cloud

    var isSignedIn: Bool { cloud?.isSignedIn == true }

    /// Sends the code and moves to the code step when Cloud accepts the email.
    func sendCode() async {
        guard let account = cloud?.account else { return }
        await account.sendCode(email: email)
        if case .codeSent = account.status { step = .signIn }
    }

    func verify(code: String) async {
        guard let account = cloud?.account else { return }
        await account.verify(code: code)
        if case .signedIn = account.status { step = .install }
    }

    // MARK: install

    /// The SSH hosts of this run.
    var cloudCandidates: [Node] { added.filter { $0.kind == .ssh } }

    /// True when the user picks the hosts for Cloud. With room 0, none go.
    var needsPick: Bool {
        guard roomLoaded, !cloudCandidates.isEmpty else { return false }
        return room != 0
    }

    /// Reads the room once. The default picks are the first hosts of the list.
    func loadRoom() async {
        guard !roomLoaded, let installer else { return }
        room = await installer.availableRoom()
        roomLoaded = true
        cloudPicks = Set(cloudCandidates.prefix(room ?? cloudCandidates.count).map(\.id))
    }

    /// The text in the info box of the host picker.
    var pickNote: String {
        let intro = "Cloud hosts get 24/7 alerts, long history and the web and phone apps."
        guard let room else { return "\(intro) SSH hosts have no limit." }
        let label = installer?.planLabel ?? "Plan"
        let name = label == "Plan" ? "Your plan" : "The \(label)"
        let hosts = "\(room) \(room == 1 ? "Cloud host" : "Cloud hosts")"
        let used = (cloud?.account.billing?.nodes ?? 0) > 0
        let plan = used ? "\(name) has room for \(room) more \(room == 1 ? "Cloud host" : "Cloud hosts")." : "\(name) takes \(hosts)."
        return "\(intro) \(plan) SSH hosts have no limit."
    }

    func togglePick(_ id: UUID) {
        if cloudPicks.contains(id) {
            cloudPicks.remove(id)
        } else if cloudPicks.count < (room ?? .max) {
            cloudPicks.insert(id)
        }
    }

    func clearPicks() { cloudPicks = [] }

    /// Leaves all hosts on SSH and ends the flow.
    func keepAllOnSSH() {
        installer?.keepOnSSH(cloudCandidates)
        installStarted = true
        finish()
    }

    func install() async {
        guard !installStarted, let installer else { return }
        installStarted = true
        await loadRoom()
        let hosts = cloudCandidates
        guard !hosts.isEmpty else { return }
        guard needsPick else {
            await installer.run(nodes: hosts)
            return
        }
        guard !cloudPicks.isEmpty else { return keepAllOnSSH() }
        installer.keepOnSSH(hosts.filter { !cloudPicks.contains($0.id) })
        await installer.run(nodes: hosts.filter { cloudPicks.contains($0.id) })
    }

    /// Hosts that stayed on SSH, or that the plan could not take.
    var hasPlanFull: Bool {
        guard let installer else { return false }
        return added.contains { installer.planFull.contains($0.id) || installer.stayed.contains($0.id) }
    }

    /// The note above the install table. Nil when no host stayed.
    var planNote: String? {
        guard let installer else { return nil }
        let stayed = cloudCandidates.filter { installer.stayed.contains($0.id) }.count
        guard stayed > 0 else { return nil }
        let moved = cloudCandidates.count - stayed
        let goes = moved == 1 ? "1 host goes" : "\(moved) hosts go"
        let rest = stayed == 1 ? "The other host stays" : "The other \(stayed) hosts stay"
        return "\(installer.planLabel): \(goes) to Cloud. \(rest) on this Mac over SSH."
    }

    /// False when this Cloud sells no plans.
    var canBuy: Bool { cloud?.account.billing?.canBuy != false }

    /// The plan that has more hosts than the current plan.
    var upgradeName: String {
        if case .signedIn(let me) = cloud?.account.status, me.org.plan.lowercased() == "solo" { return "Team" }
        return "Solo"
    }

    func upgrade() {
        guard canBuy, let account = cloud?.account else { return }
        Task { await account.openDashboard(path: "billing", using: openURL) }
    }

    // MARK: SSH

    /// Opens SSH with the key, then reads the OS and looks for systemd.
    static let sshProbe: Probe = { candidate in
        let start = Date()
        let client: SSHClient
        do {
            client = try await SSHConnectionFactory.connect(
                node: candidate.node(),
                hostKeyPrompt: HostKeyTrustPrompter.sharedPromptAdapter
            )
        } catch is SSHConnectionFactory.NoCredential {
            return (.refused, nil)
        } catch {
            return (.failed(SSHErrorRenderer.describe(error)), nil)
        }
        let ms = Int(Date().timeIntervalSince(start) * 1000)
        defer { Task { try? await client.close() } }
        guard let out = try? await client.executeCommand("uname -s; if [ -d /run/systemd/system ]; then echo systemd; fi") else {
            return (.ok(ms: ms), nil)
        }
        let lines = String(buffer: out).split(whereSeparator: \.isNewline).map(String.init)
        let os = lines.first
        if os == "Linux", !lines.contains("systemd") { return (.okNoSystemd, os) }
        return (.ok(ms: ms), os)
    }

    static let sshVerify: Verify = { node in
        _ = try await SSHBootstrap.bootstrapAndVerify(
            node: node,
            hostKeyPrompt: HostKeyTrustPrompter.sharedPromptAdapter,
            onTrust: { id, fp in Task { @MainActor in HostKeyTrustPersister.persist(nodeId: id, fingerprint: fp) } }
        )
    }
}
