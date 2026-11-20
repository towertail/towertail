import Foundation

struct SSHConfigHost: Equatable, Sendable {
    let alias: String
    let hostName: String?
    let user: String?
    let port: Int?

    var sshHost: String { hostName ?? alias }
}

/// Reads the `Host` blocks of an OpenSSH config. It skips wildcard hosts
/// and `Match` blocks. `IdentityFile` has no effect in the app.
struct SSHConfigParser {
    /// Gives the text of the files that an `Include` pattern matches. The
    /// included text gets no `Include` of its own: one level deep.
    typealias IncludeReader = (String) -> String

    static func parse(_ text: String, include: IncludeReader? = nil) -> [SSHConfigHost] {
        var hosts: [SSHConfigHost] = []
        var aliases: [String] = []
        var hostName: String?
        var user: String?
        var port: Int?
        var inMatch = false

        func flush() {
            for alias in aliases {
                hosts.append(SSHConfigHost(alias: alias, hostName: hostName, user: user, port: port))
            }
            aliases = []
            hostName = nil
            user = nil
            port = nil
        }

        func read(_ text: String, allowInclude: Bool) {
            for line in text.split(whereSeparator: \.isNewline) {
                guard let (key, value) = split(String(line)) else { continue }
                switch key {
                case "host":
                    flush()
                    inMatch = false
                    aliases = value.split(separator: " ").map(String.init).filter { !isPattern($0) }
                case "match":
                    flush()
                    inMatch = true
                case "include":
                    if allowInclude, let include {
                        for pattern in value.split(separator: " ") { read(include(String(pattern)), allowInclude: false) }
                    }
                case "hostname" where !inMatch: hostName = hostName ?? value
                case "user" where !inMatch: user = user ?? value
                case "port" where !inMatch: port = port ?? Int(value)
                default: break
                }
            }
        }

        read(text, allowInclude: true)
        flush()
        return hosts
    }

    private static func isPattern(_ alias: String) -> Bool {
        alias.contains("*") || alias.contains("?") || alias.hasPrefix("!")
    }

    /// `Key value` or `Key=value`. The key is lowercase. Comments and blank lines give nil.
    private static func split(_ line: String) -> (String, String)? {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return nil }
        guard let end = trimmed.firstIndex(where: { $0 == " " || $0 == "\t" || $0 == "=" }) else { return nil }
        let key = trimmed[..<end].lowercased()
        var value = trimmed[end...].drop { $0 == " " || $0 == "\t" || $0 == "=" }
        if let hash = value.firstIndex(of: "#") { value = value[..<hash] }
        let text = value.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: "\"", with: "")
        return text.isEmpty ? nil : (key, text)
    }
}
