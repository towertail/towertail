import Foundation

/// Processes that share a merge key, shown as one line: "node ×4 · sling · vitest run".
struct ProcCulprit: Equatable, Sendable {
    /// The process name (or the .app bundle for macOS app processes) and its folder.
    struct Key: Hashable, Sendable {
        let name: String
        /// `repo ?? cwd`. Nil when the sampler sent no folder.
        let folder: String?
    }

    /// Order and value text for one metric.
    enum Ranking: Sendable {
        case cpu, mem
        /// Disk I/O in bytes per second, by PID.
        case io(rates: [Int32: ProcIORate])

        func value(_ c: ProcCulprit) -> Double {
            switch self {
            case .cpu: c.cpuPct
            case .mem: Double(c.rss)
            case .io(let rates): c.ioRate(rates).map { $0.read + $0.write } ?? 0
            }
        }

        func text(_ c: ProcCulprit) -> String {
            switch self {
            case .cpu: return String(format: "%.0f%%", c.cpuPct)
            case .mem: return ProcessTable.formatBytes(c.rss)
            case .io(let rates):
                let r = c.ioRate(rates) ?? ProcIORate(read: 0, write: 0)
                return "R \(Self.mbps(r.read)) · W \(Self.mbps(r.write))"
            }
        }

        /// MB/s with one decimal below 10.
        static func mbps(_ bytes: Double) -> String {
            let v = bytes / 1_048_576
            return v < 10 ? String(format: "%.1f", v) : String(format: "%.0f", v)
        }
    }

    /// A folder path split for display: the repo name is the bold part.
    struct PathParts: Equatable, Sendable {
        let prefix: String
        let repoName: String
        let subPath: String
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
    /// Folder fields and owner of the top member.
    let cwd: String?
    let repo: String?
    let parent: String?
    let ppid: Int32?
    let user: String?

    init(
        name: String, pid: Int32, cmd: String?, cpuPct: Double = 0, rss: Int64 = 0,
        cwd: String? = nil, repo: String? = nil, parent: String? = nil, ppid: Int32? = nil, user: String? = nil
    ) {
        let display = Self.bundle(in: cmd) ?? (name.isEmpty ? "pid \(pid)" : name)
        let cwd = cwd.flatMap { $0.isEmpty ? nil : $0 }
        let repo = repo.flatMap { $0.isEmpty ? nil : $0 }
        self.init(
            key: Key(name: display, folder: repo ?? cwd), name: display, what: Self.what(name: name, cmd: cmd),
            count: 1, pids: [pid], cpuPct: cpuPct, rss: rss,
            cwd: cwd, repo: repo, parent: parent, ppid: ppid, user: user
        )
    }

    init(_ p: ProcSample) {
        self.init(
            name: p.name, pid: p.pid, cmd: p.cmd, cpuPct: p.cpuPct, rss: p.rss,
            cwd: p.cwd, repo: p.repo, parent: p.parent, ppid: p.ppid, user: p.user
        )
    }

    private init(
        key: Key, name: String, what: String?, count: Int, pids: [Int32], cpuPct: Double, rss: Int64,
        cwd: String?, repo: String?, parent: String?, ppid: Int32?, user: String?
    ) {
        self.key = key
        self.name = name
        self.what = what
        self.count = count
        self.pids = pids
        self.cpuPct = cpuPct
        self.rss = rss
        self.cwd = cwd
        self.repo = repo
        self.parent = parent
        self.ppid = ppid
        self.user = user
    }

    /// The name with the count: "node ×4". A `what` that starts with the name replaces it.
    var title: String {
        let times = count > 1 ? " ×\(count)" : ""
        if let what, what.hasPrefix(name + " ") { return what + times }
        return Self.cut(name, to: Self.maxNameLength) + times
    }

    /// The `what` that `title` does not already show.
    var detail: String? {
        guard let what, !what.hasPrefix(name + " ") else { return nil }
        return what
    }

    /// "node ×4 · sling (.wt/SLING-40) · vitest run", "go test ×3 · app", "pid 812".
    var label: String {
        [title, folderLabel, detail].compactMap { $0 }.joined(separator: " · ")
    }

    // MARK: - Folder

    /// Repo name (else working folder name), with ` (.wt/<task>)` for a worktree.
    var folderLabel: String? {
        guard let path = repo ?? cwd else { return nil }
        let parts = Self.segments(path)
        guard let last = parts.last else { return nil }
        // The task folder must have a folder below it.
        if parts.count > 2, let i = parts.indices.first(where: { $0 + 2 < parts.count && parts[$0] == ".wt" }) {
            return "\(last) (.wt/\(parts[i + 1]))"
        }
        return last
    }

    /// The path for the second row of the hover card: prefix, repo name, and the path below the repo.
    var pathParts: PathParts? {
        guard let root = repo ?? cwd else { return nil }
        let sep: Character = root.contains("\\") && !root.contains("/") ? "\\" : "/"
        let trimmed = Self.trimTrailing(root)
        guard let cut = trimmed.lastIndex(where: { $0 == "/" || $0 == "\\" }) else { return nil }
        let name = String(trimmed[trimmed.index(after: cut)...])
        guard !name.isEmpty else { return nil }
        let prefix = abbreviateHome(String(trimmed[...cut]))
        var sub = ""
        if repo != nil, let cwd, cwd.count > trimmed.count, cwd.hasPrefix(trimmed),
           cwd[cwd.index(cwd.startIndex, offsetBy: trimmed.count)] == sep {
            sub = Self.trimTrailing(String(cwd.dropFirst(trimmed.count)))
        }
        return PathParts(prefix: prefix, repoName: name, subPath: sub)
    }

    /// Replaces `/Users/<user>/`, `/home/<user>/` or `C:\Users\<user>\` at the start with `~`.
    private func abbreviateHome(_ prefix: String) -> String {
        guard let user, !user.isEmpty else { return prefix }
        for home in ["/Users/\(user)/", "/home/\(user)/", "C:\\Users\\\(user)\\"] where prefix.hasPrefix(home) {
            return "~" + (home.hasSuffix("/") ? "/" : "\\") + String(prefix.dropFirst(home.count))
        }
        return prefix
    }

    /// Name of the parent process. Nil for init-like parents (Q3).
    var parentLabel: String? {
        guard let parent, !parent.isEmpty else { return nil }
        if let ppid, ppid <= 1 { return nil }
        guard !["launchd", "systemd", "init"].contains(parent.lowercased()) else { return nil }
        return parent
    }

    /// Mean rate of the members that have one. Nil when none has.
    func ioRate(_ rates: [Int32: ProcIORate]) -> ProcIORate? {
        let found = pids.compactMap { rates[$0] }
        guard !found.isEmpty else { return nil }
        return ProcIORate(read: found.reduce(0) { $0 + $1.read }, write: found.reduce(0) { $0 + $1.write })
    }

    private static func segments(_ path: String) -> [String] {
        path.split(whereSeparator: { $0 == "/" || $0 == "\\" }).map(String.init)
    }

    private static func trimTrailing(_ path: String) -> String {
        var p = path
        while p.count > 1, let l = p.last, l == "/" || l == "\\" { p.removeLast() }
        return p
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
            let lead = ranking.value(c) > ranking.value(acc) ? c : acc
            merged[c.key] = ProcCulprit(
                key: acc.key, name: acc.name, what: lead.what,
                count: acc.count + 1, pids: acc.pids + c.pids,
                cpuPct: acc.cpuPct + c.cpuPct, rss: acc.rss + c.rss,
                cwd: lead.cwd, repo: lead.repo, parent: lead.parent, ppid: lead.ppid, user: lead.user
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
