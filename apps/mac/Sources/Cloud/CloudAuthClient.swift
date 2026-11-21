import Foundation

enum CloudAuthError: LocalizedError {
    case badEmail
    case wrongCode
    case locked
    case network(Error)
    case server(Int, String?)

    var errorDescription: String? {
        switch self {
        case .badEmail: return "The email address is not valid."
        case .wrongCode: return "The code is not correct."
        case .locked: return "Too many wrong codes. Send a new code."
        case .network: return "Cannot reach Towertail Cloud. Check your network."
        case .server(let status, let message): return message ?? "Cloud replied with status \(status)."
        }
    }
}

struct CloudMe: Decodable, Equatable {
    struct User: Decodable, Equatable { let email: String }
    struct Org: Decodable, Equatable { let name: String; let plan: String }
    let user: User
    let org: Org
}

struct CloudPlan: Decodable, Equatable {
    let id: String
    let maxNodes: Int
}

struct CloudBilling: Decodable, Equatable {
    let plan: CloudPlan
    let nodes: Int
    /// The host cap of the org with packs. Nil when the server is older.
    var maxNodes: Int? = nil
    /// Nil when the server is older than the payments flag. Nil means on.
    var payments: Bool? = nil

    /// Hosts that the org can still add. Nil when the cap is not known.
    var room: Int? { maxNodes.map { max(0, $0 - nodes) } }

    var canBuy: Bool { payments ?? true }
}

struct CloudOrgDeletion: Decodable, Equatable, Identifiable {
    let id: UUID
    let name: String
    let role: String
    /// `delete`, `leave` or `blocked`.
    let fate: String
    let members: Int
    let nodes: Int
    let reason: String?
}

struct CloudAccountDeletion: Decodable, Equatable {
    let email: String
    let orgs: [CloudOrgDeletion]
    let blocked: Bool
}

/// The sign-in and account routes of Towertail Cloud. These routes are
/// separate from `RemoteClient`, which needs a token for each call.
struct CloudAuthClient: Sendable {
    let endpoint: URL
    let session: URLSession

    static let defaultSession: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        return URLSession(configuration: config)
    }()

    init(endpoint: URL, session: URLSession = CloudAuthClient.defaultSession) {
        self.endpoint = endpoint
        self.session = session
    }

    func sendCode(email: String) async throws {
        struct Body: Encodable { let email: String }
        let (data, status) = try await call("POST", "/v1/auth/code", body: Body(email: email))
        switch status {
        case 200..<300: return
        case 400: throw CloudAuthError.badEmail
        default: throw CloudAuthError.server(status, Self.message(data))
        }
    }

    func verify(email: String, code: String, label: String) async throws -> String {
        struct Body: Encodable { let email: String; let code: String; let want: String; let label: String }
        struct Reply: Decodable { let token: String? }
        let body = Body(email: email, code: code, want: "token", label: label)
        let (data, status) = try await call("POST", "/v1/auth/code/verify", body: body)
        switch status {
        case 200..<300:
            guard let token = try? JSONDecoder().decode(Reply.self, from: data).token, !token.isEmpty else {
                throw CloudAuthError.server(status, "Cloud sent no token.")
            }
            return token
        case 401: throw CloudAuthError.wrongCode
        case 429: throw CloudAuthError.locked
        default: throw CloudAuthError.server(status, Self.message(data))
        }
    }

    func me(token: String) async throws -> CloudMe {
        let (data, status) = try await call("GET", "/v1/me", token: token)
        guard status < 300 else {
            throw CloudAuthError.server(status, Self.message(data))
        }
        return try decode(CloudMe.self, data, status)
    }

    /// Nil when this Cloud has no billing routes (404).
    func billing(token: String) async throws -> CloudBilling? {
        let (data, status) = try await call("GET", "/v1/billing", token: token)
        if status == 404 { return nil }
        guard status < 300 else { throw CloudAuthError.server(status, Self.message(data)) }
        return try decode(CloudBilling.self, data, status)
    }

    /// Nil when this Cloud cannot delete accounts (404).
    func accountDeletion(token: String) async throws -> CloudAccountDeletion? {
        let (data, status) = try await call("GET", "/v1/me/deletion", token: token)
        if status == 404 { return nil }
        guard status < 300 else { throw CloudAuthError.server(status, Self.message(data)) }
        return try decode(CloudAccountDeletion.self, data, status)
    }

    /// Deletes the account. The server checks the email.
    func deleteAccount(token: String, email: String) async throws {
        struct Body: Encodable { let email: String }
        let (data, status) = try await call("DELETE", "/v1/me", token: token, body: Body(email: email))
        guard status < 300 else { throw CloudAuthError.server(status, Self.message(data)) }
    }

    private func decode<T: Decodable>(_ type: T.Type, _ data: Data, _ status: Int) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: data) }
        catch { throw CloudAuthError.server(status, "Cloud sent a reply that the app cannot read.") }
    }

    private func call(_ method: String, _ path: String, token: String? = nil) async throws -> (Data, Int) {
        try await send(method, path, token: token, body: nil)
    }

    private func call<B: Encodable>(_ method: String, _ path: String, token: String? = nil, body: B) async throws -> (Data, Int) {
        try await send(method, path, token: token, body: try JSONEncoder().encode(body))
    }

    private func send(_ method: String, _ path: String, token: String?, body: Data?) async throws -> (Data, Int) {
        var req = URLRequest(url: endpoint.appendingPathComponent(path))
        req.httpMethod = method
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        do {
            let (data, resp) = try await session.data(for: req)
            guard let http = resp as? HTTPURLResponse else { throw URLError(.badServerResponse) }
            return (data, http.statusCode)
        } catch {
            throw CloudAuthError.network(error)
        }
    }

    /// The text of a wire error envelope or of an echo `{"message"}` reply.
    private static func message(_ data: Data) -> String? {
        struct Envelope: Decodable {
            struct Err: Decodable { let message: String? }
            let error: Err?
            let message: String?
        }
        let env = try? JSONDecoder().decode(Envelope.self, from: data)
        return env?.error?.message ?? env?.message
    }
}
