import Foundation

// MARK: - Wire DTOs (docs/wire.md §3.1: history, processes, disks)

struct RemoteHistoryPoint: Decodable, Sendable, Equatable {
    let ts: Date
    let value: Double
    /// Absent when 0.
    let max: Double?
}

/// The series of one node. Metrics: `cpu` (percent), `mem` (ratio),
/// `net_rx`, `net_tx`, `disk_read`, `disk_write` (bytes/s), `procs` (count).
struct RemoteHistorySeries: Decodable, Sendable {
    let nodeID: UUID
    let series: [String: [RemoteHistoryPoint]]

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case series
    }

    init(nodeID: UUID, series: [String: [RemoteHistoryPoint]]) {
        self.nodeID = nodeID
        self.series = series
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeID = try c.decode(UUID.self, forKey: .nodeID)
        series = try c.decodeIfPresent([String: [RemoteHistoryPoint]].self, forKey: .series) ?? [:]
    }

    /// Joins the metrics by timestamp, oldest first. Units follow
    /// `ServerViewModel.ingest(_:)`. A missing metric gives nil.
    func historyPoints() -> [HistoryPoint] {
        struct Row {
            var cpu: Double?, mem: Double?, rx: Double?, tx: Double?, procs: Double?
        }
        var rows: [Date: Row] = [:]
        for (metric, points) in series {
            for p in points {
                var row = rows[p.ts] ?? Row()
                switch metric {
                case "cpu": row.cpu = p.value
                case "mem": row.mem = p.value
                case "net_rx": row.rx = p.value
                case "net_tx": row.tx = p.value
                case "procs": row.procs = p.value
                default: continue
                }
                rows[p.ts] = row
            }
        }
        return rows.keys.sorted().map { t in
            let r = rows[t]!
            let rxMBps = r.rx.map(Self.mbps)
            let txMBps = r.tx.map(Self.mbps)
            let net = (rxMBps == nil && txMBps == nil)
                ? nil : min(1.0, ((rxMBps ?? 0) + (txMBps ?? 0)) / 100.0)
            return HistoryPoint(
                t: t,
                cpu: r.cpu.map { min(max($0 / 100, 0), 1) },
                mem: r.mem.map { min(max($0, 0), 1) },
                disk: nil,
                net: net,
                rxMBps: rxMBps,
                txMBps: txMBps,
                procs: r.procs
            )
        }
    }

    /// `disk_read` and `disk_write` as rows of the device total.
    func diskIORows() -> [HistoryStore.DiskIORow] {
        var read: [Date: Double] = [:]
        var write: [Date: Double] = [:]
        for p in series["disk_read"] ?? [] { read[p.ts] = p.value }
        for p in series["disk_write"] ?? [] { write[p.ts] = p.value }
        return Set(read.keys).union(write.keys).sorted().map { t in
            HistoryStore.DiskIORow(
                t: t, device: "",
                readMBps: Self.mbps(read[t] ?? 0),
                writeMBps: Self.mbps(write[t] ?? 0)
            )
        }
    }

    private static func mbps(_ bytesPerSec: Double) -> Double {
        max(bytesPerSec, 0) / 1_048_576.0
    }
}

/// `NodeHistoryMultiResponse`.
struct RemoteHistoryResponse: Decodable, Sendable {
    let nodeID: UUID
    let series: [String: [RemoteHistoryPoint]]
    let from: Date
    let to: Date
    let resolution: String
    let clamped: Bool?

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case series, from, to, resolution, clamped
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeID = try c.decode(UUID.self, forKey: .nodeID)
        series = try c.decodeIfPresent([String: [RemoteHistoryPoint]].self, forKey: .series) ?? [:]
        from = try c.decode(Date.self, forKey: .from)
        to = try c.decode(Date.self, forKey: .to)
        resolution = try c.decode(String.self, forKey: .resolution)
        clamped = try c.decodeIfPresent(Bool.self, forKey: .clamped)
    }

    func historyPoints() -> [HistoryPoint] {
        RemoteHistorySeries(nodeID: nodeID, series: series).historyPoints()
    }

    func diskIORows() -> [HistoryStore.DiskIORow] {
        RemoteHistorySeries(nodeID: nodeID, series: series).diskIORows()
    }
}

/// `FleetHistoryResponse`.
struct RemoteFleetHistory: Decodable, Sendable {
    let from: Date
    let to: Date
    let resolution: String
    let clamped: Bool?
    let nodes: [RemoteHistorySeries]

    enum CodingKeys: String, CodingKey {
        case from, to, resolution, clamped, nodes
    }

    init(from: Date, to: Date, resolution: String, clamped: Bool?, nodes: [RemoteHistorySeries]) {
        self.from = from
        self.to = to
        self.resolution = resolution
        self.clamped = clamped
        self.nodes = nodes
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        from = try c.decode(Date.self, forKey: .from)
        to = try c.decode(Date.self, forKey: .to)
        resolution = try c.decode(String.self, forKey: .resolution)
        clamped = try c.decodeIfPresent(Bool.self, forKey: .clamped)
        nodes = try c.decodeIfPresent([RemoteHistorySeries].self, forKey: .nodes) ?? []
    }
}

/// `ProcessSnapshot`. Raw rows have a PID. Rollup rows have a peak.
struct RemoteProcessSnapshot: Decodable, Sendable {
    let nodeID: UUID
    let ts: Date
    let tier: String
    let bucketS: Int
    let by: String
    let total: Int?
    let prev: Date?
    let next: Date?
    let clamped: Bool?
    let processes: [RemoteSnapshotProcess]
    /// Size of the response body. The client sets it after the decode.
    var bytes = 0

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case ts, tier, by, total, prev, next, clamped, processes
        case bucketS = "bucket_s"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeID = try c.decode(UUID.self, forKey: .nodeID)
        ts = try c.decode(Date.self, forKey: .ts)
        tier = try c.decode(String.self, forKey: .tier)
        bucketS = try c.decodeIfPresent(Int.self, forKey: .bucketS) ?? 0
        by = try c.decodeIfPresent(String.self, forKey: .by) ?? "cpu"
        total = try c.decodeIfPresent(Int.self, forKey: .total)
        prev = try c.decodeIfPresent(Date.self, forKey: .prev)
        next = try c.decodeIfPresent(Date.self, forKey: .next)
        clamped = try c.decodeIfPresent(Bool.self, forKey: .clamped)
        processes = try c.decodeIfPresent([RemoteSnapshotProcess].self, forKey: .processes) ?? []
    }
}

/// `SnapshotProcess`.
struct RemoteSnapshotProcess: Decodable, Sendable, Identifiable {
    let key: String
    let pid: Int32?
    let ppid: Int32?
    let parent: String?
    let user: String?
    let name: String
    let cmd: String
    let repo: String?
    let cwd: String?
    let cpuPct: Double
    let cpuPctMax: Double?
    let rss: UInt64
    let ioBps: Double?
    let threads: Int32?
    let state: String?
    let startTS: Date?

    /// Commands repeat, so a raw row needs its PID in the id.
    var id: String { pid.map { "\($0)#\(key)" } ?? key }

    enum CodingKeys: String, CodingKey {
        case key, pid, ppid, parent, user, name, cmd, repo, cwd, rss, threads, state
        case cpuPct = "cpu_pct"
        case cpuPctMax = "cpu_pct_max"
        case ioBps = "io_bps"
        case startTS = "start_ts"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        pid = try c.decodeIfPresent(Int32.self, forKey: .pid)
        ppid = try c.decodeIfPresent(Int32.self, forKey: .ppid)
        parent = try c.decodeIfPresent(String.self, forKey: .parent)
        user = try c.decodeIfPresent(String.self, forKey: .user)
        name = try c.decode(String.self, forKey: .name)
        cmd = try c.decodeIfPresent(String.self, forKey: .cmd) ?? ""
        repo = try c.decodeIfPresent(String.self, forKey: .repo)
        cwd = try c.decodeIfPresent(String.self, forKey: .cwd)
        cpuPct = try c.decodeIfPresent(Double.self, forKey: .cpuPct) ?? 0
        cpuPctMax = try c.decodeIfPresent(Double.self, forKey: .cpuPctMax)
        rss = try c.decodeIfPresent(UInt64.self, forKey: .rss) ?? 0
        ioBps = try c.decodeIfPresent(Double.self, forKey: .ioBps)
        threads = try c.decodeIfPresent(Int32.self, forKey: .threads)
        state = try c.decodeIfPresent(String.self, forKey: .state)
        startTS = try c.decodeIfPresent(Date.self, forKey: .startTS)
    }
}

/// `NodeProcessesResponse`. Point i of a series is at
/// `seriesFrom + i * seriesStepS`.
struct RemoteOffenders: Decodable, Sendable {
    let nodeID: UUID
    let from: Date
    let to: Date
    let clamped: Bool?
    let seriesFrom: Date?
    let seriesStepS: Int?
    let processes: [RemoteOffender]

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case from, to, clamped, processes
        case seriesFrom = "series_from"
        case seriesStepS = "series_step_s"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeID = try c.decode(UUID.self, forKey: .nodeID)
        from = try c.decode(Date.self, forKey: .from)
        to = try c.decode(Date.self, forKey: .to)
        clamped = try c.decodeIfPresent(Bool.self, forKey: .clamped)
        seriesFrom = try c.decodeIfPresent(Date.self, forKey: .seriesFrom)
        seriesStepS = try c.decodeIfPresent(Int.self, forKey: .seriesStepS)
        processes = try c.decodeIfPresent([RemoteOffender].self, forKey: .processes) ?? []
    }
}

/// `TopProcess`.
struct RemoteOffender: Decodable, Sendable, Identifiable, Equatable {
    let key: String
    let name: String
    let cmd: String
    let repo: String?
    let cwd: String?
    let parent: String?
    let cpuPctAvg: Double
    let cpuPctMax: Double
    let rssMax: UInt64
    let ioBpsAvg: Double?
    let series: RemoteProcSeries?

    var id: String { key + "\u{0}" + (repo ?? "") }

    enum CodingKeys: String, CodingKey {
        case key, name, cmd, repo, cwd, parent, series
        case cpuPctAvg = "cpu_pct_avg"
        case cpuPctMax = "cpu_pct_max"
        case rssMax = "rss_max"
        case ioBpsAvg = "io_bps_avg"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        name = try c.decode(String.self, forKey: .name)
        cmd = try c.decodeIfPresent(String.self, forKey: .cmd) ?? ""
        repo = try c.decodeIfPresent(String.self, forKey: .repo)
        cwd = try c.decodeIfPresent(String.self, forKey: .cwd)
        parent = try c.decodeIfPresent(String.self, forKey: .parent)
        cpuPctAvg = try c.decodeIfPresent(Double.self, forKey: .cpuPctAvg) ?? 0
        cpuPctMax = try c.decodeIfPresent(Double.self, forKey: .cpuPctMax) ?? 0
        rssMax = try c.decodeIfPresent(UInt64.self, forKey: .rssMax) ?? 0
        ioBpsAvg = try c.decodeIfPresent(Double.self, forKey: .ioBpsAvg)
        series = try c.decodeIfPresent(RemoteProcSeries.self, forKey: .series)
    }
}

/// `ProcSeries`. `io` is absent when the host has no I/O data.
struct RemoteProcSeries: Decodable, Sendable, Equatable {
    let cpu: [Double]
    let rss: [UInt64]
    let io: [Double]?

    enum CodingKeys: String, CodingKey { case cpu, rss, io }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        cpu = try c.decodeIfPresent([Double].self, forKey: .cpu) ?? []
        rss = try c.decodeIfPresent([UInt64].self, forKey: .rss) ?? []
        io = try c.decodeIfPresent([Double].self, forKey: .io)
    }
}

/// `ProcessDetail`. `facts` is nil when the window ends more than 1 h ago.
struct RemoteProcessDetail: Decodable, Sendable {
    let nodeID: UUID
    let key: String
    let name: String
    let cmd: String
    let repo: String?
    let from: Date
    let to: Date
    let resolution: String
    let clamped: Bool?
    let points: [RemoteProcPoint]
    let facts: RemoteProcessFacts?

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case key, name, cmd, repo, from, to, resolution, clamped, points, facts
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeID = try c.decode(UUID.self, forKey: .nodeID)
        key = try c.decode(String.self, forKey: .key)
        name = try c.decode(String.self, forKey: .name)
        cmd = try c.decodeIfPresent(String.self, forKey: .cmd) ?? ""
        repo = try c.decodeIfPresent(String.self, forKey: .repo)
        from = try c.decode(Date.self, forKey: .from)
        to = try c.decode(Date.self, forKey: .to)
        resolution = try c.decodeIfPresent(String.self, forKey: .resolution) ?? ""
        clamped = try c.decodeIfPresent(Bool.self, forKey: .clamped)
        points = try c.decodeIfPresent([RemoteProcPoint].self, forKey: .points) ?? []
        facts = try c.decodeIfPresent(RemoteProcessFacts.self, forKey: .facts)
    }
}

/// `ProcPoint`.
struct RemoteProcPoint: Decodable, Sendable {
    let ts: Date
    let cpuPct: Double
    let rss: UInt64
    let ioBps: Double?

    enum CodingKeys: String, CodingKey {
        case ts, rss
        case cpuPct = "cpu_pct"
        case ioBps = "io_bps"
    }
}

/// `ProcessFacts`.
struct RemoteProcessFacts: Decodable, Sendable {
    let seenAt: Date
    let pid: Int32
    let ppid: Int32?
    let parent: String?
    let user: String?
    let cwd: String?
    let state: String?
    let threads: Int32?
    let startTS: Date?
    let listenTCP: [UInt16]?
    let listenUDP: [UInt16]?

    enum CodingKeys: String, CodingKey {
        case pid, ppid, parent, user, cwd, state, threads
        case seenAt = "seen_at"
        case startTS = "start_ts"
        case listenTCP = "listen_tcp"
        case listenUDP = "listen_udp"
    }
}

/// `NodeDisksResponse`.
struct RemoteDiskHistory: Decodable, Sendable {
    struct Point: Decodable, Sendable {
        let ts: Date
        let used: UInt64
        let total: UInt64
        let inodesUsed: UInt64?
        let inodesTotal: UInt64?

        enum CodingKeys: String, CodingKey {
            case ts, used, total
            case inodesUsed = "inodes_used"
            case inodesTotal = "inodes_total"
        }
    }

    struct Mount: Decodable, Sendable {
        let mount: String
        let fs: String
        let points: [Point]

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            mount = try c.decode(String.self, forKey: .mount)
            fs = try c.decodeIfPresent(String.self, forKey: .fs) ?? ""
            points = try c.decodeIfPresent([Point].self, forKey: .points) ?? []
        }

        private enum CodingKeys: String, CodingKey { case mount, fs, points }
    }

    let mounts: [Mount]

    private enum CodingKeys: String, CodingKey { case mounts }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        mounts = try c.decodeIfPresent([Mount].self, forKey: .mounts) ?? []
    }

    /// Rows of the used fraction of each mount, oldest first.
    func capacityRows() -> [HistoryStore.DiskCapacityRow] {
        mounts.flatMap { m in
            m.points.map { p in
                HistoryStore.DiskCapacityRow(
                    t: p.ts, mount: m.mount,
                    frac: p.total > 0 ? Double(p.used) / Double(p.total) : 0
                )
            }
        }
        .sorted { ($0.t, $0.mount) < ($1.t, $1.mount) }
    }
}

// MARK: - Loader of the Mac app

/// Fills the series of Cloud hosts from REST. Each call goes through the
/// cache, so a quick hide and show of the popover costs nothing.
@MainActor
final class RemoteHistoryLoader: CloudHistoryLoader {
    private static let cardRange: TimeInterval = 30 * 60

    private let cache: CloudHistoryCache
    private let store: ServerStore

    init(cache: CloudHistoryCache, store: ServerStore) {
        self.cache = cache
        self.store = store
    }

    func backfillCards(_ ids: [UUID]) async {
        guard !ids.isEmpty else { return }
        do {
            let fleet = try await cache.fleet(ids, range: Self.cardRange)
            for node in fleet.nodes {
                store.merge(history: node.historyPoints(), for: node.nodeID)
            }
        } catch {
            Logger.shared.warn("cloud history: cards failed: \(error)", category: "cloud")
        }
    }

    func window(
        host: UUID, metrics: [String], window: ClosedRange<Date>, step: Duration
    ) async throws -> RemoteHistoryResponse {
        try await cache.nodeHistory(host, metrics: metrics, from: window.lowerBound, to: window.upperBound, step: step)
    }

    func disks(host: UUID, window: ClosedRange<Date>) async throws -> RemoteDiskHistory {
        try await cache.disks(host, from: window.lowerBound, to: window.upperBound)
    }

    func processesAt(host: UUID, bucket: ProcBucket, by: ProcSort) async throws -> RemoteProcessSnapshot {
        try await cache.processesAt(host, bucket: bucket, by: by)
    }

    func cachedProcessesAt(host: UUID, bucket: ProcBucket, by: ProcSort) async -> RemoteProcessSnapshot? {
        await cache.cachedProcessesAt(host, bucket: bucket, by: by)
    }

    func offenders(host: UUID, window: ClosedRange<Date>, by: ProcSort) async throws -> RemoteOffenders {
        try await cache.offenders(host, from: window.lowerBound, to: window.upperBound, by: by)
    }

    func process(host: UUID, key: String, window: ClosedRange<Date>, step: Duration) async throws -> RemoteProcessDetail {
        try await cache.process(host, key: key, from: window.lowerBound, to: window.upperBound, step: step)
    }
}

@MainActor
enum CloudHistoryError {
    /// The server text of an error envelope, else the error text.
    static func message(_ error: Error) -> String {
        if case RemoteClient.RemoteError.http(_, let body) = error,
           let text = RemoteClient.errorMessage(Data(body.utf8)) {
            return text
        }
        return error.localizedDescription
    }
}
