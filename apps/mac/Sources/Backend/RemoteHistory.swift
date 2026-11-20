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

/// One item of `NodeProcessesResponse.processes`.
struct RemoteTopProcess: Decodable, Sendable, Identifiable, Equatable {
    let name: String
    let cmd: String
    let cpuPctAvg: Double
    let cpuPctMax: Double
    let rssMax: UInt64

    var id: String { name + "\u{0}" + cmd }

    enum CodingKeys: String, CodingKey {
        case name, cmd
        case cpuPctAvg = "cpu_pct_avg"
        case cpuPctMax = "cpu_pct_max"
        case rssMax = "rss_max"
    }
}

/// `NodeDisksResponse`.
struct RemoteDiskHistory: Decodable, Sendable {
    struct Point: Decodable, Sendable {
        let ts: Date
        let used: UInt64
        let total: UInt64
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
    static let fullViewRange: TimeInterval = 2 * 3600

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

    func loadFullView(host: UUID, metric: Metric) async throws {
        if metric == .disk {
            async let disks = cache.disks(host, range: Self.fullViewRange)
            async let io = cache.nodeHistory(host, metrics: ["disk_read", "disk_write"], range: Self.fullViewRange)
            let (d, r) = try await (disks, io)
            store.merge(diskCapacity: d.capacityRows(), diskIO: r.diskIORows(), for: host)
        } else {
            let r = try await cache.nodeHistory(
                host, metrics: ["cpu", "mem", "net_rx", "net_tx", "procs"], range: Self.fullViewRange
            )
            store.merge(history: r.historyPoints(), for: host)
        }
    }

    func pastProcesses(host: UUID, from: Date, to: Date) async throws -> [RemoteTopProcess] {
        try await cache.processes(host, from: from, to: to)
    }
}
