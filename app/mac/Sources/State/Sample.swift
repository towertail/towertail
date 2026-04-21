import Foundation

struct Sample: Decodable, Sendable {
    let v: Int
    let ts: Date
    let host: HostInfo
    let cpu: CPUInfo
    let mem: MemInfo
    let swap: MemInfo
    let disks: [DiskSample]?
    let diskIO: DiskIOInfo?
    let net: NetInfo?
    let procs: ProcList?
    let errors: [String]

    enum CodingKeys: String, CodingKey {
        case v, ts, host, cpu, mem, swap, disks
        case diskIO = "disk_io"
        case net, procs, errors
    }

    init(
        v: Int,
        ts: Date,
        host: HostInfo,
        cpu: CPUInfo,
        mem: MemInfo,
        swap: MemInfo,
        disks: [DiskSample]? = nil,
        diskIO: DiskIOInfo? = nil,
        net: NetInfo? = nil,
        procs: ProcList? = nil,
        errors: [String]
    ) {
        self.v = v
        self.ts = ts
        self.host = host
        self.cpu = cpu
        self.mem = mem
        self.swap = swap
        self.disks = disks
        self.diskIO = diskIO
        self.net = net
        self.procs = procs
        self.errors = errors
    }
}

struct HostInfo: Decodable, Sendable {
    let name: String
    let os: String
    let arch: String
    let kernel: String
    let uptimeS: Int64
    let sampler: String
    let machineID: String?

    enum CodingKeys: String, CodingKey {
        case name, os, arch, kernel
        case uptimeS = "uptime_s"
        case sampler
        case machineID = "machine_id"
    }
}

struct CPUInfo: Decodable, Sendable {
    let pct: Double
    let load1: Double
    let load5: Double
    let load15: Double
    let cores: Int
    let userMs: Int64?
    let systemMs: Int64?
    let idleMs: Int64?
    let iowaitMs: Int64?
    let irqMs: Int64?
    let niceMs: Int64?
    let stealMs: Int64?
    let totalMs: Int64?

    enum CodingKeys: String, CodingKey {
        case pct
        case load1 = "load_1"
        case load5 = "load_5"
        case load15 = "load_15"
        case cores
        case userMs = "user_ms"
        case systemMs = "system_ms"
        case idleMs = "idle_ms"
        case iowaitMs = "iowait_ms"
        case irqMs = "irq_ms"
        case niceMs = "nice_ms"
        case stealMs = "steal_ms"
        case totalMs = "total_ms"
    }

    /// Sum of "doing work" time — anything that isn't idle/iowait.
    var busyMs: Int64? {
        guard let totalMs, let idleMs else { return nil }
        let iow = iowaitMs ?? 0
        return totalMs - idleMs - iow
    }

    init(
        pct: Double,
        load1: Double,
        load5: Double,
        load15: Double,
        cores: Int,
        userMs: Int64? = nil,
        systemMs: Int64? = nil,
        idleMs: Int64? = nil,
        iowaitMs: Int64? = nil,
        irqMs: Int64? = nil,
        niceMs: Int64? = nil,
        stealMs: Int64? = nil,
        totalMs: Int64? = nil
    ) {
        self.pct = pct
        self.load1 = load1
        self.load5 = load5
        self.load15 = load15
        self.cores = cores
        self.userMs = userMs
        self.systemMs = systemMs
        self.idleMs = idleMs
        self.iowaitMs = iowaitMs
        self.irqMs = irqMs
        self.niceMs = niceMs
        self.stealMs = stealMs
        self.totalMs = totalMs
    }
}

struct MemInfo: Decodable, Sendable {
    let used: Int64
    let total: Int64
}

struct DiskSample: Decodable, Sendable {
    let mount: String
    let fs: String
    let used: Int64
    let total: Int64
}

struct NetInfo: Decodable, Sendable {
    let rxBps: Int64
    let txBps: Int64
    let rxCum: Int64
    let txCum: Int64

    enum CodingKeys: String, CodingKey {
        case rxBps = "rx_bps"
        case txBps = "tx_bps"
        case rxCum = "rx_cum"
        case txCum = "tx_cum"
    }
}

struct DiskIOInfo: Decodable, Sendable {
    let readBps: Int64
    let writeBps: Int64
    let readCum: Int64
    let writeCum: Int64
    let devices: [DiskIODevice]?

    enum CodingKeys: String, CodingKey {
        case readBps = "read_bps"
        case writeBps = "write_bps"
        case readCum = "read_cum"
        case writeCum = "write_cum"
        case devices
    }
}

struct DiskIODevice: Decodable, Sendable, Hashable {
    let name: String
    let readBps: Int64
    let writeBps: Int64
    let readCum: Int64
    let writeCum: Int64

    enum CodingKeys: String, CodingKey {
        case name
        case readBps = "read_bps"
        case writeBps = "write_bps"
        case readCum = "read_cum"
        case writeCum = "write_cum"
    }
}

struct ProcList: Codable, Sendable {
    let root: Bool
    let topN: Int
    let total: Int
    let visible: Int
    let items: [ProcSample]

    enum CodingKeys: String, CodingKey {
        case root
        case topN = "top_n"
        case total, visible, items
    }
}

struct ProcSample: Codable, Sendable, Identifiable {
    let pid: Int32
    let ppid: Int32?
    let name: String
    let cmd: String?
    let user: String?
    let cpuPct: Double
    let rss: Int64
    let threads: Int32?
    let state: String?
    let startTS: Date?
    let readBytes: Int64?
    let writeBytes: Int64?

    var id: Int32 { pid }

    enum CodingKeys: String, CodingKey {
        case pid, ppid, name, cmd, user
        case cpuPct = "cpu_pct"
        case rss, threads, state
        case startTS = "start_ts"
        case readBytes = "read_bytes"
        case writeBytes = "write_bytes"
    }

    init(
        pid: Int32,
        ppid: Int32? = nil,
        name: String,
        cmd: String? = nil,
        user: String? = nil,
        cpuPct: Double,
        rss: Int64,
        threads: Int32? = nil,
        state: String? = nil,
        startTS: Date? = nil,
        readBytes: Int64? = nil,
        writeBytes: Int64? = nil
    ) {
        self.pid = pid
        self.ppid = ppid
        self.name = name
        self.cmd = cmd
        self.user = user
        self.cpuPct = cpuPct
        self.rss = rss
        self.threads = threads
        self.state = state
        self.startTS = startTS
        self.readBytes = readBytes
        self.writeBytes = writeBytes
    }
}

enum SampleCodec {
    static func decoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let s = try c.decode(String.self)
            let fmt = ISO8601DateFormatter()
            fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let parsed = fmt.date(from: s) { return parsed }
            fmt.formatOptions = [.withInternetDateTime]
            if let parsed = fmt.date(from: s) { return parsed }
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "invalid ts: \(s)")
        }
        return d
    }
}
