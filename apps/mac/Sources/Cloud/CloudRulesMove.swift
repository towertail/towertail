import Foundation
import Observation

/// Moves the local rules of this Mac to the cloud. It drives the
/// "Move your rules to the cloud" sheet.
@MainActor
@Observable
final class CloudRulesMove {
    enum Phase: Equatable { case loading, pick, sending, failed(String) }

    /// Set after the sheet opened by itself one time.
    static let offeredKey = "cloudRulesMoveOffered"

    var isPresented = false
    private(set) var phase: Phase = .pick
    private(set) var rows: [RulesDiffRow] = []
    private(set) var picks: Set<String> = []
    private(set) var toast: String?

    @ObservationIgnored private let settings: ServerSettings
    @ObservationIgnored private let nodes: NodeStore
    @ObservationIgnored private let rules: @MainActor () -> CloudRules?
    @ObservationIgnored private let client: @MainActor () -> RemoteClient?
    @ObservationIgnored private let isAdmin: @MainActor () -> Bool
    @ObservationIgnored private let device: String
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private var cloud: RemoteRulesView?

    init(
        settings: ServerSettings,
        nodes: NodeStore,
        rules: @escaping @MainActor () -> CloudRules?,
        client: @escaping @MainActor () -> RemoteClient?,
        isAdmin: @escaping @MainActor () -> Bool,
        device: String,
        defaults: UserDefaults = .standard
    ) {
        self.settings = settings
        self.nodes = nodes
        self.rules = rules
        self.client = client
        self.isAdmin = isAdmin
        self.device = device
        self.defaults = defaults
    }

    convenience init(session: CloudSession, settings: ServerSettings, nodes: NodeStore) {
        self.init(
            settings: settings,
            nodes: nodes,
            rules: { session.rules },
            client: { session.client },
            isAdmin: { session.account.me?.isAdmin == true },
            device: CloudSession.deviceName
        )
    }

    /// Signed in with the admin role.
    var canMove: Bool { client() != nil && isAdmin() }

    var pickedCount: Int { rows.filter { picks.contains($0.id) }.count }

    /// Loads the rules of the cloud and opens the sheet.
    func open() async {
        isPresented = true
        phase = .loading
        await load()
    }

    /// Opens the sheet one time after sign-in, when a local rule differs
    /// from the cloud. It stays closed when the load fails.
    func offerOnce() async {
        guard !defaults.bool(forKey: Self.offeredKey), canMove, !isPresented else { return }
        guard case .pick = await fetch(), !rows.isEmpty else { return }
        defaults.set(true, forKey: Self.offeredKey)
        isPresented = true
    }

    func toggle(_ id: String) {
        if picks.contains(id) { picks.remove(id) } else { picks.insert(id) }
    }

    func start() async {
        guard let client = client(), let cloud, pickedCount > 0 else { return }
        switch phase {
        case .pick, .failed: break
        default: return
        }
        phase = .sending
        let picked = rows.filter { picks.contains($0.id) }
        do {
            _ = try await client.importRules(RulesDiff.importBody(picked, cloud: cloud, device: device))
            await rules()?.refresh()
            let n = picked.count
            toast = "Moved \(n) \(n == 1 ? "rule" : "rules") to the cloud. The Mac now reads them from the cloud."
            close()
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    func close() {
        isPresented = false
        phase = .pick
    }

    func clearToast() { toast = nil }

    private func load() async {
        phase = await fetch()
    }

    @discardableResult
    private func fetch() async -> Phase {
        guard let client = client() else { return .failed("Sign in to Towertail Cloud first.") }
        do {
            let view = try await client.rules()
            cloud = view
            rows = RulesDiff.rows(settings: settings, nodes: nodes.nodes, cloud: view)
            picks = Set(rows.map(\.id))
            return .pick
        } catch {
            rows = []
            picks = []
            return .failed(error.localizedDescription)
        }
    }
}
