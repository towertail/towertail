import Foundation

struct Sample: Decodable, Sendable {
    let v: Int
    let ts: Date
    let host: HostInfo
    let cpu: CPUInfo
    let mem: MemInfo
    let swap: MemInfo
    let disks: [DiskSample]?
    let net: NetInfo?
    let errors: [String]
}

struct HostInfo: Decodable, Sendable {
    let name: String
    let os: String
    let arch: String
    let kernel: String
    let uptimeS: Int64
    let agent: String
    let machineID: String?

    enum CodingKeys: String, CodingKey {
        case name, os, arch, kernel
        case uptimeS = "uptime_s"
        case agent
        case machineID = "machine_id"
    }
}

struct CPUInfo: Decodable, Sendable {
    let pct: Double
    let load1: Double
    let load5: Double
    let load15: Double
    let cores: Int

    enum CodingKeys: String, CodingKey {
        case pct
        case load1 = "load_1"
        case load5 = "load_5"
        case load15 = "load_15"
        case cores
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
