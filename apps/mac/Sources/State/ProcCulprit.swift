import Foundation

/// Processes that share a merge key, shown as one line: "node ×4 · vitest run".
struct ProcCulprit: Equatable, Sendable {
    /// The process name, or the .app bundle for macOS app processes.
    struct Key: Hashable, Sendable { let name: String }

    /// Order and value text for one metric.
    enum Ranking: Sendable {
        case cpu, mem

        func value(_ c: ProcCulprit) -> Double {
            switch self {
            case .cpu: c.cpuPct
            case .mem: Double(c.rss)
            }
        }

        func text(_ c: ProcCulprit) -> String {
            switch self {
            case .cpu: String(format: "%.0f%%", c.cpuPct)
            case .mem: ProcessTable.formatBytes(c.rss)
            }
        }
    }

    static let maxNameLength = 24
    static let maxWhatLength = 28

    let key: Key
    /// "pid N" when the name is empty. The bundle name for app processes.
    let name: String
    /// From the member with the highest ranking value.
    let what: String?
    let count: Int
    let pids: [Int32]
    /// Sum of the members, `top` convention.
    let cpuPct: Double
    let rss: Int64

    init(name: String, pid: Int32, cmd: String?, cpuPct: Double = 0, rss: Int64 = 0) {
        let display = Self.bundle(in: cmd) ?? (name.isEmpty ? "pid \(pid)" : name)
        self.init(
            key: Key(name: display), name: display, what: Self.what(name: name, cmd: cmd),
            count: 1, pids: [pid], cpuPct: cpuPct, rss: rss
        )
    }

    init(_ p: ProcSample) {
        self.init(name: p.name, pid: p.pid, cmd: p.cmd, cpuPct: p.cpuPct, rss: p.rss)
    }

    private init(key: Key, name: String, what: String?, count: Int, pids: [Int32], cpuPct: Double, rss: Int64) {
        self.key = key
        self.name = name
        self.what = what
        self.count = count
        self.pids = pids
        self.cpuPct = cpuPct
        self.rss = rss
    }

    /// "node ×4 · vitest run", "go test ×3", "pid 812".
    var label: String {
        let n = Self.cut(name, to: Self.maxNameLength)
        let times = count > 1 ? " ×\(count)" : ""
        guard let what else { return n + times }
        if what.hasPrefix(name + " ") { return what + times }
        return n + times + " · " + what
    }

    /// Merges by key, sorts by the ranking (ties by name A-Z) and keeps `limit`.
    static func top(_ items: [ProcSample], by ranking: Ranking, limit: Int) -> [ProcCulprit] {
        var merged: [Key: ProcCulprit] = [:]
        for p in items {
            let c = ProcCulprit(p)
            guard let acc = merged[c.key] else {
                merged[c.key] = c
                continue
            }
            let useNew = ranking.value(c) > ranking.value(acc)
            merged[c.key] = ProcCulprit(
                key: acc.key, name: acc.name, what: useNew ? c.what : acc.what,
                count: acc.count + 1, pids: acc.pids + c.pids,
                cpuPct: acc.cpuPct + c.cpuPct, rss: acc.rss + c.rss
            )
        }
        return merged.values
            .sorted {
                let a = ranking.value($0), b = ranking.value($1)
                return a != b ? a > b : $0.name < $1.name
            }
            .prefix(limit)
            .map { $0 }
    }

    // MARK: - What

    private static let interpreters: Set<String> = ["node", "python", "ruby", "java", "bun", "deno", "go"]
    private static let subcommandTools: Set<String> = ["go", "bun", "deno"]
    private static let valueFlags: [String: Set<String>] = [
        "node": ["-r", "--require", "--import", "--loader"],
        "python": ["-W", "-X"],
        "java": ["-cp", "-classpath", "--class-path"],
    ]

    /// The script and its first words, for known interpreters. Nil otherwise.
    static func what(name: String, cmd: String?) -> String? {
        guard let cmd, !cmd.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        let tool = normalized(name)
        guard interpreters.contains(tool) else { return nil }
        var tokens = Array(tokenize(cmd).dropFirst())
        let skip = valueFlags[tool] ?? []

        var script: String?
        while !tokens.isEmpty, script == nil {
            let t = tokens.removeFirst()
            if t.hasPrefix("-") {
                if (tool == "node" && (t == "-e" || t == "--eval")) || (tool == "python" && t == "-c") {
                    return nil
                }
                if (tool == "python" && t == "-m") || (tool == "java" && t == "-jar") {
                    guard !tokens.isEmpty else { return nil }
                    script = basename(tokens.removeFirst())
                } else if skip.contains(t), !tokens.isEmpty {
                    tokens.removeFirst()
                }
                continue
            }
            if subcommandTools.contains(tool), isPlainWord(t) {
                script = "\(tool) \(t)"
            } else if tool == "java" {
                script = String(t.split(separator: ".").last ?? Substring(t))
            } else {
                script = basename(t)
            }
        }
        guard var result = script, !result.isEmpty else { return nil }
        var added = 0
        for t in tokens {
            guard added < 2, isPlainWord(t) else { break }
            result += " " + t
            added += 1
        }
        return cut(result, to: maxWhatLength)
    }

    private static func normalized(_ name: String) -> String {
        var n = name.lowercased()
        if n.hasSuffix(".exe") { n.removeLast(4) }
        while let l = n.last, l.isNumber || l == "." { n.removeLast() }
        return n
    }

    /// Splits on white space. A pair of double quotes keeps one token.
    private static func tokenize(_ s: String) -> [String] {
        var tokens: [String] = []
        var cur = ""
        var quoted = false
        for ch in s {
            if ch == "\"" {
                quoted.toggle()
            } else if ch.isWhitespace && !quoted {
                if !cur.isEmpty { tokens.append(cur); cur = "" }
            } else {
                cur.append(ch)
            }
        }
        if !cur.isEmpty { tokens.append(cur) }
        return tokens
    }

    private static func basename(_ s: String) -> String {
        String(s.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last ?? Substring(s))
    }

    private static func isPlainWord(_ s: String) -> Bool {
        guard let first = s.first, first.isLetter, s.count <= 20 else { return false }
        return s.allSatisfy { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" || $0 == ":" }
    }

    private static func cut(_ s: String, to limit: Int) -> String {
        s.count > limit ? String(s.prefix(limit - 1)) + "…" : s
    }

    /// The first `.app` bundle of the executable path, such as "Google Chrome.app".
    private static func bundle(in cmd: String?) -> String? {
        guard let cmd, cmd.hasPrefix("/"), let r = cmd.range(of: ".app/") else { return nil }
        let head = cmd[..<r.lowerBound]
        // A space before a slash starts an argument, not the path.
        guard !head.contains(" /") else { return nil }
        let start = head.lastIndex(of: "/").map { head.index(after: $0) } ?? head.startIndex
        let name = head[start...]
        return name.isEmpty ? nil : name + ".app"
    }
}
