import Foundation

/// One peer as surfaced to the bulk-import UI. The full Tailscale status
/// payload has many more fields; we only lift what the wizard needs.
struct TailscalePeer: Sendable, Hashable, Identifiable {
    var id: String { publicKey.isEmpty ? hostname : publicKey }
    let publicKey: String
    let hostname: String
    let magicDNSName: String
    let ips: [String]
    let os: String
    let online: Bool
    let tags: [String]
    let isSelf: Bool

    /// Best-effort first IPv4 / 100.x Tailscale address. Empty string if none.
    var primaryIP: String {
        ips.first(where: { $0.hasPrefix("100.") }) ?? ips.first ?? ""
    }

    /// MagicDNS short name if the full name was provided, stripped of its
    /// trailing dot; otherwise falls back to the hostname.
    var displayMagicDNS: String {
        let trimmed = magicDNSName.hasSuffix(".") ? String(magicDNSName.dropLast()) : magicDNSName
        return trimmed.isEmpty ? hostname : trimmed
    }
}

enum TailscaleLocalAPIError: LocalizedError {
    case notInstalled
    case noToken
    case httpFailed(status: Int, body: String)
    case transport(Error)
    case decode(Error)

    var errorDescription: String? {
        switch self {
        case .notInstalled:
            return "Tailscale isn't installed, or the app isn't running under your user account."
        case .noToken:
            return "Couldn't find the Tailscale LocalAPI token — is Tailscale running and logged in?"
        case .httpFailed(let status, let body):
            let trimmed = body.trimmingCharacters(in: .whitespacesAndNewlines)
            return "Tailscale LocalAPI returned HTTP \(status): \(trimmed.isEmpty ? "(empty)" : trimmed)"
        case .transport(let e):
            return "Tailscale LocalAPI request failed: \(e.localizedDescription)"
        case .decode(let e):
            return "Tailscale LocalAPI response couldn't be parsed: \(e.localizedDescription)"
        }
    }
}

/// Talks to the macOS Tailscale app's local-only HTTP API. The app keeps
/// a random port + bearer token in
/// `~/Library/Group Containers/W5364U7YZB.group.io.tailscale.ipn.macos/sameuserproof-<port>-<token>`;
/// we read the filename, then basic-auth against `127.0.0.1:<port>`.
enum TailscaleLocalAPI {
    static let groupContainer = "W5364U7YZB.group.io.tailscale.ipn.macos"
    static let appPath = "/Applications/Tailscale.app"

    /// True iff the app bundle exists AND we can read a sameuserproof file.
    /// Called at Source-step render time so the tile can dim itself.
    static func isAvailable() -> Bool {
        let fm = FileManager.default
        guard fm.fileExists(atPath: appPath) else { return false }
        return tokenInfo() != nil
    }

    /// Returns (port, token) if a sameuserproof file exists and is readable.
    static func tokenInfo() -> (port: Int, token: String)? {
        let fm = FileManager.default
        let dir = (NSHomeDirectory() as NSString)
            .appendingPathComponent("Library/Group Containers/\(groupContainer)")
        guard let entries = try? fm.contentsOfDirectory(atPath: dir) else { return nil }
        for entry in entries where entry.hasPrefix("sameuserproof-") {
            // sameuserproof-<port>-<token>
            let parts = entry.split(separator: "-", maxSplits: 2, omittingEmptySubsequences: false)
            guard parts.count == 3,
                  let port = Int(parts[1]),
                  !parts[2].isEmpty
            else { continue }
            return (port, String(parts[2]))
        }
        return nil
    }

    /// Fetches the tailnet status and returns Self + Peers as a flat list.
    /// Self is marked `isSelf = true` so the caller can filter it out.
    static func fetchPeers() async throws -> [TailscalePeer] {
        guard FileManager.default.fileExists(atPath: appPath) else {
            throw TailscaleLocalAPIError.notInstalled
        }
        guard let info = tokenInfo() else {
            throw TailscaleLocalAPIError.noToken
        }
        guard let url = URL(string: "http://127.0.0.1:\(info.port)/localapi/v0/status") else {
            throw TailscaleLocalAPIError.noToken
        }
        var request = URLRequest(url: url)
        // Basic auth: empty user, token as password.
        let auth = ":\(info.token)".data(using: .utf8)!.base64EncodedString()
        request.setValue("Basic \(auth)", forHTTPHeaderField: "Authorization")
        request.timeoutInterval = 10

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await URLSession.shared.data(for: request)
        } catch {
            throw TailscaleLocalAPIError.transport(error)
        }
        if let http = response as? HTTPURLResponse, http.statusCode != 200 {
            let body = String(data: data, encoding: .utf8) ?? ""
            throw TailscaleLocalAPIError.httpFailed(status: http.statusCode, body: body)
        }
        do {
            let status = try JSONDecoder().decode(RawStatus.self, from: data)
            return status.asPeers()
        } catch {
            throw TailscaleLocalAPIError.decode(error)
        }
    }
}

// MARK: - Raw API shape
// The LocalAPI status payload is a superset of `tailscale status --json`.
// We lift just what the wizard needs and keep Codable keys close to the
// wire format so drift is easy to spot.
private struct RawStatus: Decodable {
    let Self_: RawPeer?
    let Peer: [String: RawPeer]?

    enum CodingKeys: String, CodingKey {
        case Self_ = "Self"
        case Peer
    }

    func asPeers() -> [TailscalePeer] {
        var out: [TailscalePeer] = []
        if let s = Self_ { out.append(s.toPeer(isSelf: true)) }
        if let peers = Peer {
            // Sort by hostname to get stable UI ordering across calls.
            let sorted = peers.values.sorted { lhs, rhs in
                lhs.hostName.localizedCaseInsensitiveCompare(rhs.hostName) == .orderedAscending
            }
            for p in sorted {
                out.append(p.toPeer(isSelf: false))
            }
        }
        return out
    }
}

private struct RawPeer: Decodable {
    let ID: String?
    let PublicKey: String?
    let HostName: String
    let DNSName: String?
    let OS: String?
    let TailscaleIPs: [String]?
    let Online: Bool?
    let Tags: [String]?

    var hostName: String { HostName }

    func toPeer(isSelf: Bool) -> TailscalePeer {
        TailscalePeer(
            publicKey: PublicKey ?? ID ?? HostName,
            hostname: HostName,
            magicDNSName: DNSName ?? "",
            ips: TailscaleIPs ?? [],
            os: OS ?? "",
            online: Online ?? false,
            tags: Tags ?? [],
            isSelf: isSelf
        )
    }
}
