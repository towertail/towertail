import Foundation

// Lease-mode frames of `/v1/stream` (docs/wire.md §4).

/// The part of a sample that a `detail` frame carries.
enum DetailTab: String, Codable, Sendable {
    case processes, ports, disks, health
}

/// Slow facts of a host. The server sends them in the first summary of a
/// lease and after a change.
struct HostFacts: Codable, Sendable, Equatable {
    let os: String
    let arch: String
    let kernel: String?
    let cores: Int
    let memTotal: Int64
    let uptimeS: Int64
    let sampler: String

    enum CodingKeys: String, CodingKey {
        case os, arch, kernel, cores, sampler
        case memTotal = "mem_total"
        case uptimeS = "uptime_s"
    }
}

/// `HealthInfo` without the zombie parents, plus the worst inode ratio.
struct HealthBrief: Decodable, Sendable {
    let info: HealthInfo
    let inodes: Double?

    private enum ExtraKeys: String, CodingKey { case inodes }

    init(info: HealthInfo, inodes: Double? = nil) {
        self.info = info
        self.inodes = inodes
    }

    init(from decoder: Decoder) throws {
        info = try HealthInfo(from: decoder)
        inodes = try decoder.container(keyedBy: ExtraKeys.self).decodeIfPresent(Double.self, forKey: .inodes)
    }
}

/// The short sample of a leased host. `cpu` is a percent. `mem` and `disk`
/// are ratios. `rx` and `tx` are bytes each second.
struct LiveSummary: Decodable, Sendable {
    let ts: Date
    let cpu: Double
    let mem: Double
    let disk: Double
    let rx: Int64
    let tx: Int64
    let load: Double
    var procs: Int? = nil
    var health: HealthBrief? = nil
    var host: HostFacts? = nil
}

/// The parts of a sample for the open tab.
struct LiveDetail: Decodable, Sendable {
    let ts: Date
    var procs: ProcList? = nil
    var ports: PortList? = nil
    var disks: [DiskSample]? = nil
    var diskIO: DiskIOInfo? = nil
    var health: HealthInfo? = nil

    enum CodingKeys: String, CodingKey {
        case ts, procs, ports, disks, health
        case diskIO = "disk_io"
    }
}

struct ViewDetail: Encodable, Sendable, Equatable {
    let host: UUID
    let tab: DetailTab
}

/// The `view` frame that the client sends. `.empty` ends the lease.
struct LiveView: Encodable, Sendable, Equatable {
    var hosts: [UUID]
    var detail: ViewDetail?
    var ttl: Int = 60

    static let empty = LiveView(hosts: [], detail: nil)

    private enum CodingKeys: String, CodingKey { case type, hosts, detail, ttl }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode("view", forKey: .type)
        try c.encode(hosts, forKey: .hosts)
        try c.encodeIfPresent(detail, forKey: .detail)
        try c.encode(ttl, forKey: .ttl)
    }
}

/// A `lease` frame. `reason` is "limit" when the server ends the lease
/// because of its limit for each user.
struct LeaseNotice: Decodable, Sendable, Equatable {
    let state: String
    let reason: String?

    var isLimit: Bool { reason == "limit" }
}
