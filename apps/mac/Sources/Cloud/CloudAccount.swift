import Foundation
import Observation

/// Keeps the Cloud user token. Tests use a fake.
protocol TokenKeychain {
    func set(_ token: String, account: String) throws
    func get(account: String) throws -> String?
    func delete(account: String) throws
}

struct CloudKeychain: TokenKeychain {
    static let service = "com.towertail.cloud"

    func set(_ token: String, account: String) throws {
        try KeychainStore.set(token, service: Self.service, account: account)
    }

    func get(account: String) throws -> String? {
        try KeychainStore.get(service: Self.service, account: account)
    }

    func delete(account: String) throws {
        try KeychainStore.delete(service: Self.service, account: account)
    }
}

/// The Cloud account of the user: the sign-in with an email code, the user
/// token and the plan.
@MainActor
@Observable
final class CloudAccount {
    enum Status: Equatable {
        case signedOut
        case codeSent(email: String, at: Date)
        case signedIn(CloudMe)
        case failed(String)
    }

    static let resendSeconds = 60
    static let endpointKey = "cloudEndpoint"
    static let emailKey = "cloudEmail"
    static let defaultEndpoint = URL(string: "https://api.towertail.com")!

    private(set) var status: Status = .signedOut
    private(set) var billing: CloudBilling?
    /// The email that got the last code. The code field is for this email.
    private(set) var pendingEmail: String?
    @ObservationIgnored var onStatusChange: ((Status) -> Void)?

    let endpoint: URL
    @ObservationIgnored private let keychain: TokenKeychain
    @ObservationIgnored private let client: CloudAuthClient
    @ObservationIgnored private let now: () -> Date
    @ObservationIgnored private let retryDelay: Duration
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private var lastSentAt: Date?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private(set) var token: String?

    init(
        endpoint: URL,
        keychain: TokenKeychain,
        client: CloudAuthClient,
        now: @escaping () -> Date = Date.init,
        retryDelay: Duration = .seconds(30),
        defaults: UserDefaults = .standard
    ) {
        self.endpoint = endpoint
        self.keychain = keychain
        self.client = client
        self.now = now
        self.retryDelay = retryDelay
        self.defaults = defaults
    }

    static func configuredEndpoint(_ defaults: UserDefaults = .standard) -> URL {
        defaults.string(forKey: endpointKey).flatMap(URL.init(string:)) ?? defaultEndpoint
    }

    /// The Keychain account: the API host.
    private var account: String {
        let host = endpoint.host ?? endpoint.absoluteString
        return endpoint.port.map { "\(host):\($0)" } ?? host
    }

    /// The web dashboard: `api.` in the host becomes `app.`.
    var dashboardURL: URL {
        guard var parts = URLComponents(url: endpoint, resolvingAgainstBaseURL: false),
              let host = parts.host, host.hasPrefix("api.") else { return endpoint }
        parts.host = "app." + host.dropFirst(4)
        return parts.url ?? endpoint
    }

    /// Seconds until the user can ask for a new code. 0 means now.
    var resendWait: Int {
        guard let lastSentAt else { return 0 }
        return max(0, Self.resendSeconds - Int(now().timeIntervalSince(lastSentAt)))
    }

    func sendCode(email: String) async {
        let email = email.trimmingCharacters(in: .whitespaces)
        if email == pendingEmail, resendWait > 0 { return }
        do {
            try await client.sendCode(email: email)
            pendingEmail = email
            lastSentAt = now()
            defaults.set(email, forKey: Self.emailKey)
            set(.codeSent(email: email, at: now()))
        } catch {
            set(.failed(error.localizedDescription))
        }
    }

    func verify(code: String) async {
        guard let email = pendingEmail else { return }
        let token: String
        do {
            token = try await client.verify(email: email, code: code, label: "Towertail for Mac")
            try keychain.set(token, account: account)
        } catch {
            set(.failed(error.localizedDescription))
            return
        }
        self.token = token
        pendingEmail = nil
        lastSentAt = nil
        await load(token: token)
    }

    /// Runs at launch. A 401 from Cloud signs out. Other errors keep the
    /// token and try again after a delay.
    func restore() async {
        retryTask?.cancel()
        retryTask = nil
        await attemptRestore()
    }

    private func attemptRestore() async {
        guard let stored = try? keychain.get(account: account) else { return }
        token = stored
        await load(token: stored)
    }

    func signOut() {
        retryTask?.cancel()
        retryTask = nil
        try? keychain.delete(account: account)
        token = nil
        billing = nil
        pendingEmail = nil
        lastSentAt = nil
        set(.signedOut)
    }

    /// Reads the plan again. It keeps the old value on an error.
    @discardableResult
    func refreshBilling() async -> CloudBilling? {
        guard let token else { return billing }
        if let fresh = try? await client.billing(token: token) { billing = fresh }
        return billing
    }

    /// What an account delete does to each org. Nil when this Cloud cannot delete accounts.
    func deletion() async throws -> CloudAccountDeletion? {
        guard let token else { throw LifecycleError.notSignedIn }
        return try await client.accountDeletion(token: token)
    }

    /// Deletes the account in Cloud, then signs out. An error keeps the sign-in.
    func deleteAccount(email: String) async throws {
        guard let token else { throw LifecycleError.notSignedIn }
        try await client.deleteAccount(token: token, email: email)
        signOut()
    }

    private func load(token: String) async {
        do {
            let me = try await client.me(token: token)
            billing = try? await client.billing(token: token)
            set(.signedIn(me))
        } catch CloudAuthError.server(401, _) {
            signOut()
        } catch {
            set(.failed(error.localizedDescription))
            scheduleRetry()
        }
    }

    private func scheduleRetry() {
        retryTask?.cancel()
        retryTask = Task { [weak self, retryDelay] in
            try? await Task.sleep(for: retryDelay)
            guard !Task.isCancelled else { return }
            await self?.attemptRestore()
        }
    }

    private func set(_ next: Status) {
        status = next
        onStatusChange?(next)
    }
}
