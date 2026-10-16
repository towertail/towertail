import Foundation

// Scan lines of `towertail-sampler scan` (sampler wire-format §3.4).
// Keys are snake_case. The scan version `v` is separate from the sample `v`.

/// What to scan. An empty or nil root is the home folder of the user.
struct DiskScanRequest: Codable, Sendable, Equatable {
    var root: String?
    var maxDepth: Int?
    var minBytes: Int64?

    init(root: String? = nil, maxDepth: Int? = nil, minBytes: Int64? = nil) {
        self.root = root
        self.maxDepth = maxDepth
        self.minBytes = minBytes
    }

    enum CodingKeys: String, CodingKey {
        case root
        case maxDepth = "max_depth"
        case minBytes = "min_bytes"
    }

    /// Key for the store: "~" for the home folder, else the root path.
    var scope: String {
        guard let root, !root.isEmpty else { return "~" }
        return root
    }

    /// Arguments after the binary path.
    var samplerArguments: [String] {
        var args = ["scan"]
        if let root, !root.isEmpty, root != "~" { args += ["--root", root] }
        if let maxDepth { args += ["--max-depth", String(maxDepth)] }
        if let minBytes { args += ["--min-bytes", "\(minBytes)B"] }
        return args
    }

    /// One shell command line for an SSH exec channel. Each argument is
    /// single-quoted, except the binary path (it starts with `~`).
    func shellCommand(binary: String) -> String {
        ([binary] + samplerArguments.map(Self.shellQuote)).joined(separator: " ")
    }

    static func shellQuote(_ s: String) -> String {
        let safe = CharacterSet(charactersIn:
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._/@%+=:,-")
        if !s.isEmpty, s.unicodeScalars.allSatisfy({ safe.contains($0) }) { return s }
        return "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}

struct DiskScanTop: Codable, Sendable, Equatable {
    var path: String
    var b: Int64
}

struct DiskScanProgress: Codable, Sendable, Equatable {
    var files: Int64
    var dirs: Int64
    var bytes: Int64
    var noAccessDirs: Int64
    var current: String
    var elapsedMs: Int64
    /// Used space of the filesystem that holds the root. 0 when unknown.
    var usedBytes: Int64
    var top: [DiskScanTop]

    init(
        files: Int64 = 0, dirs: Int64 = 0, bytes: Int64 = 0, noAccessDirs: Int64 = 0,
        current: String = "", elapsedMs: Int64 = 0, usedBytes: Int64 = 0, top: [DiskScanTop] = []
    ) {
        self.files = files
        self.dirs = dirs
        self.bytes = bytes
        self.noAccessDirs = noAccessDirs
        self.current = current
        self.elapsedMs = elapsedMs
        self.usedBytes = usedBytes
        self.top = top
    }

    enum CodingKeys: String, CodingKey {
        case files, dirs, bytes, current, top
        case noAccessDirs = "no_access_dirs"
        case elapsedMs = "elapsed_ms"
        case usedBytes = "used_bytes"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        files = try c.decodeIfPresent(Int64.self, forKey: .files) ?? 0
        dirs = try c.decodeIfPresent(Int64.self, forKey: .dirs) ?? 0
        bytes = try c.decodeIfPresent(Int64.self, forKey: .bytes) ?? 0
        noAccessDirs = try c.decodeIfPresent(Int64.self, forKey: .noAccessDirs) ?? 0
        current = try c.decodeIfPresent(String.self, forKey: .current) ?? ""
        elapsedMs = try c.decodeIfPresent(Int64.self, forKey: .elapsedMs) ?? 0
        usedBytes = try c.decodeIfPresent(Int64.self, forKey: .usedBytes) ?? 0
        top = try c.decodeIfPresent([DiskScanTop].self, forKey: .top) ?? []
    }
}

struct DiskScanFS: Codable, Sendable, Equatable {
    var mount: String
    var total: Int64
    var used: Int64
    var free: Int64
}

struct DiskScanNoAccess: Codable, Sendable, Equatable {
    var dirs: [String]
    var count: Int64
    /// fs.used − scanned_bytes when the root is a mount point, else nil.
    var bytesEst: Int64?

    enum CodingKeys: String, CodingKey {
        case dirs, count
        case bytesEst = "bytes_est"
    }

    init(dirs: [String] = [], count: Int64 = 0, bytesEst: Int64? = nil) {
        self.dirs = dirs
        self.count = count
        self.bytesEst = bytesEst
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        dirs = try c.decodeIfPresent([String].self, forKey: .dirs) ?? []
        count = try c.decodeIfPresent(Int64.self, forKey: .count) ?? Int64(dirs.count)
        bytesEst = try c.decodeIfPresent(Int64.self, forKey: .bytesEst)
    }
}

/// Category of a tree node. Unknown values decode as `.other`.
enum DiskCategory: String, Codable, Sendable, CaseIterable {
    case code, ai, cache, sdk, docs, other, none

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = DiskCategory(rawValue: raw) ?? .other
    }

    var label: String {
        switch self {
        case .code: return "Code projects"
        case .ai: return "AI models"
        case .cache: return "Package caches"
        case .sdk: return "SDKs & toolchains"
        case .docs: return "Documents & media"
        case .other: return "System & other"
        case .none: return "No access"
        }
    }
}

/// One node of the pruned tree.
struct DiskNode: Codable, Sendable, Equatable {
    var n: String
    var b: Int64
    var cat: DiskCategory
    /// Set only on nodes that a recommendation points to.
    var id: String?
    var f: Bool?
    var o: Bool?
    var c: [DiskNode]?

    init(n: String, b: Int64, cat: DiskCategory = .other, id: String? = nil,
         f: Bool? = nil, o: Bool? = nil, c: [DiskNode]? = nil) {
        self.n = n
        self.b = b
        self.cat = cat
        self.id = id
        self.f = f
        self.o = o
        self.c = c
    }

    var name: String { n }
    var bytes: Int64 { b }
    var isFile: Bool { f ?? false }
    var isOther: Bool { o ?? false }
    var children: [DiskNode] { c ?? [] }

    /// Ids of this node and all nodes below it.
    var subtreeIDs: Set<String> {
        var out: Set<String> = []
        if let id { out.insert(id) }
        for child in children { out.formUnion(child.subtreeIDs) }
        return out
    }
}

struct DiskRecCmd: Codable, Sendable, Equatable {
    var sh: [String]
    var pwsh: [String]

    init(sh: [String] = [], pwsh: [String] = []) {
        self.sh = sh
        self.pwsh = pwsh
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        sh = try c.decodeIfPresent([String].self, forKey: .sh) ?? []
        pwsh = try c.decodeIfPresent([String].self, forKey: .pwsh) ?? []
    }

    enum CodingKeys: String, CodingKey { case sh, pwsh }
}

enum DiskRecGroup: String, Codable, Sendable {
    case safe, review
}

/// One cleanup recommendation.
struct DiskRec: Codable, Sendable, Equatable, Identifiable {
    var id: String
    var group: DiskRecGroup
    var label: String
    /// For display. Starts with `~` (or `%USERPROFILE%`) below home.
    var path: String
    var bytes: Int64
    var elevated: Bool
    var nodeIDs: [String]
    var cmd: DiskRecCmd

    enum CodingKeys: String, CodingKey {
        case id, group, label, path, bytes, elevated, cmd
        case nodeIDs = "node_ids"
    }

    init(id: String, group: DiskRecGroup, label: String, path: String, bytes: Int64,
         elevated: Bool = false, nodeIDs: [String] = [], cmd: DiskRecCmd) {
        self.id = id
        self.group = group
        self.label = label
        self.path = path
        self.bytes = bytes
        self.elevated = elevated
        self.nodeIDs = nodeIDs
        self.cmd = cmd
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        group = (try? c.decode(DiskRecGroup.self, forKey: .group)) ?? .review
        label = try c.decode(String.self, forKey: .label)
        path = try c.decodeIfPresent(String.self, forKey: .path) ?? ""
        bytes = try c.decodeIfPresent(Int64.self, forKey: .bytes) ?? 0
        elevated = try c.decodeIfPresent(Bool.self, forKey: .elevated) ?? false
        nodeIDs = try c.decodeIfPresent([String].self, forKey: .nodeIDs) ?? []
        cmd = try c.decodeIfPresent(DiskRecCmd.self, forKey: .cmd) ?? DiskRecCmd()
    }
}

/// The result line. It is the last line of a scan that did not fail
/// and was not cancelled.
struct DiskScanResult: Codable, Sendable, Equatable {
    var root: String
    var os: String
    var user: String
    var hostname: String
    var home: String
    var startedTs: String
    var durationMs: Int64
    var files: Int64
    var dirs: Int64
    var scannedBytes: Int64
    var fs: DiskScanFS?
    var noAccess: DiskScanNoAccess
    var tree: DiskNode
    var recs: [DiskRec]
    var errors: [String]

    enum CodingKeys: String, CodingKey {
        case root, os, user, hostname, home, files, dirs, fs, tree, recs, errors
        case startedTs = "started_ts"
        case durationMs = "duration_ms"
        case scannedBytes = "scanned_bytes"
        case noAccess = "no_access"
    }

    init(root: String, os: String, user: String = "", hostname: String, home: String = "",
         startedTs: String, durationMs: Int64 = 0, files: Int64 = 0, dirs: Int64 = 0,
         scannedBytes: Int64, fs: DiskScanFS? = nil, noAccess: DiskScanNoAccess = DiskScanNoAccess(),
         tree: DiskNode, recs: [DiskRec] = [], errors: [String] = []) {
        self.root = root
        self.os = os
        self.user = user
        self.hostname = hostname
        self.home = home
        self.startedTs = startedTs
        self.durationMs = durationMs
        self.files = files
        self.dirs = dirs
        self.scannedBytes = scannedBytes
        self.fs = fs
        self.noAccess = noAccess
        self.tree = tree
        self.recs = recs
        self.errors = errors
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        root = try c.decode(String.self, forKey: .root)
        os = try c.decodeIfPresent(String.self, forKey: .os) ?? ""
        user = try c.decodeIfPresent(String.self, forKey: .user) ?? ""
        hostname = try c.decodeIfPresent(String.self, forKey: .hostname) ?? ""
        home = try c.decodeIfPresent(String.self, forKey: .home) ?? ""
        startedTs = try c.decodeIfPresent(String.self, forKey: .startedTs) ?? ""
        durationMs = try c.decodeIfPresent(Int64.self, forKey: .durationMs) ?? 0
        files = try c.decodeIfPresent(Int64.self, forKey: .files) ?? 0
        dirs = try c.decodeIfPresent(Int64.self, forKey: .dirs) ?? 0
        scannedBytes = try c.decodeIfPresent(Int64.self, forKey: .scannedBytes) ?? 0
        fs = try c.decodeIfPresent(DiskScanFS.self, forKey: .fs)
        noAccess = try c.decodeIfPresent(DiskScanNoAccess.self, forKey: .noAccess) ?? DiskScanNoAccess()
        tree = try c.decode(DiskNode.self, forKey: .tree)
        recs = try c.decodeIfPresent([DiskRec].self, forKey: .recs) ?? []
        errors = try c.decodeIfPresent([String].self, forKey: .errors) ?? []
    }

    var isWindows: Bool { os == "windows" }

    /// Start time of the scan, parsed from `started_ts`.
    var startedAt: Date? { SampleCodec.parseRFC3339UTC(startedTs) }

    /// End time of the scan. Falls back to `now` when `started_ts` is bad.
    func finishedAt(fallback now: Date = Date()) -> Date {
        guard let s = startedAt else { return now }
        return s.addingTimeInterval(Double(durationMs) / 1000)
    }

    /// Commands of a recommendation for the shell of the host OS.
    func commands(for rec: DiskRec) -> [String] {
        isWindows ? rec.cmd.pwsh : rec.cmd.sh
    }
}

/// Server job state of a remote scan (docs/wire.md §3.1).
struct DiskScanJob: Codable, Sendable, Equatable, Identifiable {
    enum State: String, Codable, Sendable {
        case queued, running, done, failed, cancelled, expired

        init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = State(rawValue: raw) ?? .failed
        }

        var isFinal: Bool {
            switch self {
            case .queued, .running: return false
            case .done, .failed, .cancelled, .expired: return true
            }
        }
    }

    var id: String
    var nodeId: String
    var state: State
    var root: String
    var maxDepth: Int?
    var minBytes: Int64?
    var progress: DiskScanProgress?
    var error: String
    var cancelRequested: Bool
    var createdTs: Date?
    var startedTs: Date?
    var finishedTs: Date?
    var resultBytes: Int64

    enum CodingKeys: String, CodingKey {
        case id, state, root, progress, error
        case nodeId = "node_id"
        case maxDepth = "max_depth"
        case minBytes = "min_bytes"
        case cancelRequested = "cancel_requested"
        case createdTs = "created_ts"
        case startedTs = "started_ts"
        case finishedTs = "finished_ts"
        case resultBytes = "result_bytes"
    }

    init(id: String, nodeId: String, state: State, root: String = "", maxDepth: Int? = nil,
         minBytes: Int64? = nil, progress: DiskScanProgress? = nil, error: String = "",
         cancelRequested: Bool = false, createdTs: Date? = nil, startedTs: Date? = nil,
         finishedTs: Date? = nil, resultBytes: Int64 = 0) {
        self.id = id
        self.nodeId = nodeId
        self.state = state
        self.root = root
        self.maxDepth = maxDepth
        self.minBytes = minBytes
        self.progress = progress
        self.error = error
        self.cancelRequested = cancelRequested
        self.createdTs = createdTs
        self.startedTs = startedTs
        self.finishedTs = finishedTs
        self.resultBytes = resultBytes
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        nodeId = try c.decodeIfPresent(String.self, forKey: .nodeId) ?? ""
        state = try c.decode(State.self, forKey: .state)
        root = try c.decodeIfPresent(String.self, forKey: .root) ?? ""
        maxDepth = try c.decodeIfPresent(Int.self, forKey: .maxDepth)
        minBytes = try c.decodeIfPresent(Int64.self, forKey: .minBytes)
        progress = try c.decodeIfPresent(DiskScanProgress.self, forKey: .progress)
        error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
        cancelRequested = try c.decodeIfPresent(Bool.self, forKey: .cancelRequested) ?? false
        createdTs = Self.date(c, .createdTs)
        startedTs = Self.date(c, .startedTs)
        finishedTs = Self.date(c, .finishedTs)
        resultBytes = try c.decodeIfPresent(Int64.self, forKey: .resultBytes) ?? 0
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(nodeId, forKey: .nodeId)
        try c.encode(state, forKey: .state)
        try c.encode(root, forKey: .root)
        try c.encodeIfPresent(maxDepth, forKey: .maxDepth)
        try c.encodeIfPresent(minBytes, forKey: .minBytes)
        try c.encodeIfPresent(progress, forKey: .progress)
        try c.encode(error, forKey: .error)
        try c.encode(cancelRequested, forKey: .cancelRequested)
        try c.encodeIfPresent(createdTs.map(SampleCodec.formatRFC3339UTC), forKey: .createdTs)
        try c.encodeIfPresent(startedTs.map(SampleCodec.formatRFC3339UTC), forKey: .startedTs)
        try c.encodeIfPresent(finishedTs.map(SampleCodec.formatRFC3339UTC), forKey: .finishedTs)
        try c.encode(resultBytes, forKey: .resultBytes)
    }

    /// Accepts RFC 3339 with and without fractional seconds, and null.
    private static func date(_ c: KeyedDecodingContainer<CodingKeys>, _ key: CodingKeys) -> Date? {
        guard let s = try? c.decodeIfPresent(String.self, forKey: key), !s.isEmpty else { return nil }
        if let d = SampleCodec.parseRFC3339UTC(s) { return d }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }
}

/// One event of a running scan.
enum DiskScanEvent: Sendable, Equatable {
    case progress(DiskScanProgress)
    case result(DiskScanResult)
    case error(String)
    case cancelled
    /// Remote scans only: the server job changed.
    case job(DiskScanJob)

    var isFinal: Bool {
        switch self {
        case .result, .error, .cancelled: return true
        case .progress, .job: return false
        }
    }

    /// Decodes one NDJSON scan line.
    static func decodeLine(_ data: Data) throws -> DiskScanEvent {
        struct Head: Decodable { let t: String; let v: Int? }
        let decoder = JSONDecoder()
        let head = try decoder.decode(Head.self, from: data)
        if let v = head.v, v > DiskScanEvent.supportedVersion {
            throw DiskScanError.unsupportedVersion(v)
        }
        switch head.t {
        case "progress":
            return .progress(try decoder.decode(DiskScanProgress.self, from: data))
        case "result":
            return .result(try decoder.decode(DiskScanResult.self, from: data))
        case "error":
            struct Msg: Decodable { let message: String? }
            return .error(try decoder.decode(Msg.self, from: data).message ?? "scan failed")
        case "cancelled":
            return .cancelled
        default:
            throw DiskScanError.unknownLine(head.t)
        }
    }

    static let supportedVersion = 1
}

enum DiskScanError: LocalizedError, Equatable {
    case binaryMissing
    case unsupportedVersion(Int)
    case unknownLine(String)
    case failed(String)
    case exited(code: Int32, stderr: String)
    case noOutput(seconds: Int)
    case misconfigured(String)
    case scansOff(String)
    case alreadyRunning(DiskScanJob?)
    case remote(String)

    var errorDescription: String? {
        switch self {
        case .binaryMissing: return "The bundled sampler was not found."
        case .unsupportedVersion(let v): return "The sampler sent scan version \(v). Update Towertail."
        case .unknownLine(let t): return "The sampler sent an unknown line \"\(t)\"."
        case .failed(let msg): return msg
        case .exited(let code, let stderr):
            let s = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            if s.contains("No help topic for 'scan'") || s.contains("flag provided but not defined") {
                return "The sampler on this host has no scan command. Update the sampler."
            }
            return "The sampler stopped with exit code \(code)\(s.isEmpty ? "" : ": \(s)")."
        case .noOutput(let s): return "The sampler sent no data for \(s) s."
        case .misconfigured(let msg): return msg
        case .scansOff(let msg): return msg
        case .alreadyRunning: return "A scan already runs on this node."
        case .remote(let msg): return msg
        }
    }
}

/// Size text for scan results. Decimal units, as the sampler uses.
enum DiskSize {
    static func text(_ bytes: Int64) -> String {
        let v = Double(max(bytes, 0))
        if v < 1_000 { return String(format: "%.0f B", v) }
        if v < 1_000_000 { return String(format: "%.0f KB", v / 1e3) }
        if v < 1_000_000_000 { return String(format: "%.0f MB", v / 1e6) }
        if v < 100_000_000_000 { return String(format: "%.1f GB", v / 1e9) }
        if v < 1_000_000_000_000 { return String(format: "%.0f GB", v / 1e9) }
        return String(format: "%.1f TB", v / 1e12)
    }

    /// Count text such as "412k" or "2.9M".
    static func count(_ n: Int64) -> String {
        let v = Double(max(n, 0))
        if v < 1_000 { return String(format: "%.0f", v) }
        if v < 1_000_000 { return String(format: "%.0fk", v / 1e3) }
        return String(format: "%.1fM", v / 1e6)
    }
}
