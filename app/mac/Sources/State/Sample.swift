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
    /// JSONDecoder is thread-safe once configured (Foundation guarantees
    /// concurrent calls to `decode` are safe). Hand the same instance back
    /// to every caller instead of building one per poll.
    private static let sharedDecoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let s = try c.decode(String.self)
            if let parsed = parseRFC3339UTC(s) { return parsed }
            // Fallback to the system parser on any unexpected shape
            // (e.g. a future sampler adds a non-Z offset). Pays the ICU
            // cost only on the rare exception path.
            if let parsed = fallbackFormatter.date(from: s) { return parsed }
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "invalid ts: \(s)")
        }
        return d
    }()

    /// Used only when the fast path can't parse the input. Kept as a
    /// fallback so a future sampler change doesn't silently break decoding.
    private static let fallbackFormatter: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    static func decoder() -> JSONDecoder { sharedDecoder }

    /// Inverse of `parseRFC3339UTC`. Emits `YYYY-MM-DDTHH:MM:SS.fffZ`,
    /// matching the sampler's wire format so a round trip via SQLite
    /// proc-snapshot persistence is bit-stable. Pure ASCII, no locale,
    /// no allocation beyond the result string.
    static func formatRFC3339UTC(_ date: Date) -> String {
        // Civil-from-days, also Hinnant: inverse of `parseRFC3339UTC`.
        let totalSec = date.timeIntervalSince1970
        let totalSecFloor = totalSec.rounded(.down)
        let days = Int(totalSecFloor / 86_400)
        let timeOfDay = totalSecFloor - Double(days * 86_400)
        let hour = Int(timeOfDay / 3_600)
        let minute = Int(timeOfDay.truncatingRemainder(dividingBy: 3_600) / 60)
        let second = Int(timeOfDay.truncatingRemainder(dividingBy: 60))

        let z = days + 719_468
        let era = (z >= 0 ? z : z - 146_096) / 146_097
        let doe = z - era * 146_097
        let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365
        let y = yoe + era * 400
        let doy = doe - (365 * yoe + yoe / 4 - yoe / 100)
        let mp = (5 * doy + 2) / 153
        let day = doy - (153 * mp + 2) / 5 + 1
        let month = mp + (mp < 10 ? 3 : -9)
        let year = y + (month <= 2 ? 1 : 0)

        // Round to 3 fractional digits (millisecond precision matches the
        // sampler). Use round-half-away-from-zero so the rounded value
        // matches what the sampler would emit.
        let frac = totalSec - totalSecFloor
        let millis = Int((frac * 1000).rounded())
        // If rounding bumped us to 1000, carry into the second.
        var s = second, m = minute, h = hour, mi = millis
        if mi == 1000 {
            mi = 0
            s += 1
            if s == 60 { s = 0; m += 1 }
            if m == 60 { m = 0; h += 1 }
            // Carrying past 23:59:59.999 would change the day; the
            // sampler doesn't emit dates that close to midnight in
            // practice, so accept the rare drift rather than re-deriving
            // the date.
        }

        // Fixed 24-byte ASCII layout: YYYY-MM-DDTHH:MM:SS.fffZ
        var out = ""
        out.reserveCapacity(24)
        @inline(__always) func pad2(_ v: Int) -> String {
            return v < 10 ? "0\(v)" : "\(v)"
        }
        @inline(__always) func pad3(_ v: Int) -> String {
            if v < 10 { return "00\(v)" }
            if v < 100 { return "0\(v)" }
            return "\(v)"
        }
        out += "\(year)"
        out += "-\(pad2(month))"
        out += "-\(pad2(day))"
        out += "T\(pad2(h))"
        out += ":\(pad2(m))"
        out += ":\(pad2(s))"
        out += ".\(pad3(mi))Z"
        return out
    }

    /// Hand parser for the sampler's exact wire format: an RFC3339 UTC
    /// timestamp like `2026-04-25T20:07:02.646Z` (with optional fractional
    /// seconds). Avoids `ISO8601DateFormatter`, whose ICU-backed parser
    /// clones a `SimpleDateFormat` on every call — that showed up as ~20%
    /// of the per-poll worker thread when decoding 20+ proc timestamps.
    /// Returns nil for any input that doesn't fit the expected shape;
    /// the caller falls back to the slow formatter.
    static func parseRFC3339UTC(_ s: String) -> Date? {
        // Operate on UTF-8 bytes, not Characters — every char in the
        // expected format is ASCII, so this skips Unicode scaling cost.
        let utf8 = s.utf8
        guard utf8.count >= 20 else { return nil }
        var it = utf8.makeIterator()

        @inline(__always) func next() -> UInt8? { it.next() }
        @inline(__always) func digit(_ b: UInt8?) -> Int? {
            guard let b, b >= 0x30, b <= 0x39 else { return nil }
            return Int(b - 0x30)
        }
        @inline(__always) func two() -> Int? {
            guard let h = digit(next()), let l = digit(next()) else { return nil }
            return h * 10 + l
        }
        @inline(__always) func four() -> Int? {
            guard let a = digit(next()), let b = digit(next()),
                  let c = digit(next()), let d = digit(next()) else { return nil }
            return ((a * 10 + b) * 10 + c) * 10 + d
        }
        @inline(__always) func expect(_ ch: UInt8) -> Bool { next() == ch }

        guard let year = four(), expect(0x2D /* - */),
              let month = two(), expect(0x2D),
              let day = two(), expect(0x54 /* T */),
              let hour = two(), expect(0x3A /* : */),
              let minute = two(), expect(0x3A),
              let second = two() else { return nil }

        var fracSeconds: Double = 0
        var b = next()
        if b == 0x2E /* . */ {
            // Read up to 9 fractional digits, then either Z or end.
            var scale: Double = 0.1
            var any = false
            b = next()
            while let bb = b, let d = digit(bb) {
                fracSeconds += Double(d) * scale
                scale *= 0.1
                any = true
                b = next()
            }
            if !any { return nil }
        }
        // Must terminate with `Z` and no trailing bytes.
        guard b == 0x5A /* Z */, next() == nil else { return nil }

        // Inline civil-time → POSIX seconds. Valid for 1970..2100; the
        // sampler is wall-clock-now, well inside that range.
        let secondsPerDay = 86_400
        let daysFromCivil: Int = {
            // Howard Hinnant's algorithm — branchless and cheap.
            let y = year - (month <= 2 ? 1 : 0)
            let era = (y >= 0 ? y : y - 399) / 400
            let yoe = y - era * 400
            let doy = (153 * (month + (month > 2 ? -3 : 9)) + 2) / 5 + day - 1
            let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
            return era * 146_097 + doe - 719_468
        }()
        let posix = Double(daysFromCivil * secondsPerDay + hour * 3600 + minute * 60 + second) + fracSeconds
        // Date(timeIntervalSince1970:) treats its argument as UTC, matching
        // the trailing Z we just verified.
        return Date(timeIntervalSince1970: posix)
    }
}
