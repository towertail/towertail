import Foundation

struct ServerCandidate: Identifiable, Hashable, Sendable {
    enum Source: Hashable, Sendable { case tailscale, sshConfig, hand }

    enum TestResult: Hashable, Sendable {
        case untested
        case ok(ms: Int)
        case okNoSystemd
        case refused
        case failed(String)

        var works: Bool {
            switch self {
            case .ok, .okNoSystemd: return true
            case .untested, .refused, .failed: return false
            }
        }
    }

    let id: String
    let name: String
    let address: String
    let user: String
    var port: Int?
    var os: String?
    var source: Source
    var test: TestResult = .untested

    func node() -> Node {
        Node(displayName: name, kind: .ssh, sshUser: user, sshHost: address, sshPort: port)
    }
}

protocol ServerDiscovery: Sendable {
    func tailscale() async throws -> [ServerCandidate]
    func sshConfig() throws -> [ServerCandidate]
}

struct DefaultServerDiscovery: ServerDiscovery {
    var home: URL = URL(fileURLWithPath: NSHomeDirectory())
    var userName: String = NSUserName()

    /// Linux and macOS peers. Windows hosts have no 24/7 sampler.
    func tailscale() async throws -> [ServerCandidate] {
        try await TailscaleLocalAPI.fetchPeers()
            .filter { !$0.isSelf && ["linux", "macos"].contains($0.os.lowercased()) }
            .map { peer in
                ServerCandidate(
                    id: "ts:\(peer.id)",
                    name: peer.hostname,
                    address: peer.primaryIP.isEmpty ? peer.displayMagicDNS : peer.primaryIP,
                    user: userName,
                    os: peer.os,
                    source: .tailscale
                )
            }
    }

    func sshConfig() throws -> [ServerCandidate] {
        let dir = home.appendingPathComponent(".ssh")
        guard let data = FileManager.default.contents(atPath: dir.appendingPathComponent("config").path) else { return [] }
        let text = String(decoding: data, as: UTF8.self)
        return SSHConfigParser.parse(text, include: { read(include: $0, in: dir) }).map { host in
            ServerCandidate(
                id: "ssh:\(host.alias)",
                name: host.alias,
                address: host.sshHost,
                user: host.user ?? userName,
                port: host.port,
                source: .sshConfig
            )
        }
    }

    private func read(include pattern: String, in dir: URL) -> String {
        let expanded = pattern.hasPrefix("~") ? home.path + pattern.dropFirst() : pattern
        let full = expanded.hasPrefix("/") ? expanded : dir.appendingPathComponent(expanded).path
        var found = glob_t()
        defer { globfree(&found) }
        guard glob(full, 0, nil, &found) == 0 else { return "" }
        return (0..<Int(found.gl_pathc)).compactMap { i in
            found.gl_pathv[i].flatMap { FileManager.default.contents(atPath: String(cString: $0)) }
        }
        .map { String(decoding: $0, as: UTF8.self) }
        .joined(separator: "\n")
    }
}
