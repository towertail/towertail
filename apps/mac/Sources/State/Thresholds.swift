import Foundation

/// Warn and critical levels for one metric. Units depend on the metric.
struct ThresholdPair: Codable, Sendable, Equatable {
    var warn: Double
    var critical: Double

    init(warn: Double, critical: Double) {
        self.warn = warn
        self.critical = max(warn, critical)
    }
}

/// Host health limits other than process and zombie counts.
/// PID use, open files and inodes are fractions; pressure is PSI avg10 percent.
struct HealthLimits: Codable, Sendable, Equatable {
    var pids: ThresholdPair
    var files: ThresholdPair
    var inodes: ThresholdPair
    /// Warns on PSI `some`, goes critical on `full`.
    var memPressure: ThresholdPair
    var ioPressure: ThresholdPair

    static let defaults = HealthLimits(
        pids: ThresholdPair(warn: 0.70, critical: 0.90),
        files: ThresholdPair(warn: 0.80, critical: 0.95),
        inodes: ThresholdPair(warn: 0.85, critical: 0.95),
        memPressure: ThresholdPair(warn: 10, critical: 20),
        ioPressure: ThresholdPair(warn: 20, critical: 40)
    )

    init(pids: ThresholdPair, files: ThresholdPair, inodes: ThresholdPair,
         memPressure: ThresholdPair, ioPressure: ThresholdPair) {
        self.pids = pids
        self.files = files
        self.inodes = inodes
        self.memPressure = memPressure
        self.ioPressure = ioPressure
    }

    enum CodingKeys: String, CodingKey { case pids, files, inodes, memPressure, ioPressure }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = Self.defaults
        self.init(
            pids: try c.decodeIfPresent(ThresholdPair.self, forKey: .pids) ?? d.pids,
            files: try c.decodeIfPresent(ThresholdPair.self, forKey: .files) ?? d.files,
            inodes: try c.decodeIfPresent(ThresholdPair.self, forKey: .inodes) ?? d.inodes,
            memPressure: try c.decodeIfPresent(ThresholdPair.self, forKey: .memPressure) ?? d.memPressure,
            ioPressure: try c.decodeIfPresent(ThresholdPair.self, forKey: .ioPressure) ?? d.ioPressure
        )
    }
}

/// Metrics that have a warn/critical pair a node can override.
enum ThresholdMetric: String, CaseIterable, Identifiable, Sendable {
    case cpu, mem, disk, procs, zombies
    case pids, files, inodes, memPressure, ioPressure

    static let usage: [ThresholdMetric] = [.cpu, .mem, .disk]
    static let health: [ThresholdMetric] = [.procs, .zombies, .pids, .files, .inodes, .memPressure, .ioPressure]
    /// Health checks behind "More checks" in the Alerts tab.
    static let moreHealth: [ThresholdMetric] = [.pids, .files, .inodes, .memPressure, .ioPressure]

    var id: String { rawValue }

    var title: String {
        switch self {
        case .cpu: return "CPU"
        case .mem: return "Memory"
        case .disk: return "Disk"
        case .procs: return "Processes"
        case .zombies: return "Zombies"
        case .pids: return "PID use"
        case .files: return "Open files"
        case .inodes: return "Inodes"
        case .memPressure: return "Memory pressure"
        case .ioPressure: return "I/O pressure"
        }
    }

    var help: String? {
        switch self {
        case .memPressure: return "PSI avg10. Warns on some, goes critical on full."
        case .ioPressure: return "PSI avg10, full."
        default: return nil
        }
    }

    /// Fraction metrics are stored as 0...1 and show as percent.
    var isFraction: Bool {
        switch self {
        case .cpu, .mem, .disk, .pids, .files, .inodes: return true
        case .procs, .zombies, .memPressure, .ioPressure: return false
        }
    }

    var isCount: Bool { self == .procs || self == .zombies }

    /// Factor from the stored value to the value the user edits.
    var scale: Double { isFraction ? 100 : 1 }
    var unit: String { isCount ? "" : "%" }
    var range: ClosedRange<Double> { isCount ? 1...1_000_000 : 1...100 }

    /// The alert rule that times this metric.
    var alertMetric: Metric {
        switch self {
        case .cpu: return .cpu
        case .mem: return .mem
        case .disk: return .disk
        default: return .health
        }
    }

    func format(_ v: Double) -> String {
        if isCount { return Int(v).formatted(.number.grouping(.automatic)) }
        return "\(Int((v * scale).rounded()))%"
    }
}

struct MetricThresholds: Sendable, Equatable, Codable {
    var cpuWarn: Double
    var cpuCritical: Double
    var memWarn: Double
    var memCritical: Double
    var diskWarn: Double
    var diskCritical: Double
    /// Host health counts. See `HealthStatus` for the other health rules.
    var procsWarn: Int
    var procsCritical: Int
    var zombiesWarn: Int
    var zombiesCritical: Int
    var health: HealthLimits

    static let defaultProcsWarn = 5000
    static let defaultProcsCritical = 20000
    static let defaultZombiesWarn = 200
    static let defaultZombiesCritical = 2000

    static let defaults = MetricThresholds(
        cpuWarn: 0.75, cpuCritical: 0.90,
        memWarn: 0.75, memCritical: 0.90,
        diskWarn: 0.85, diskCritical: 0.95
    )

    enum CodingKeys: String, CodingKey {
        case cpuWarn, cpuCritical, memWarn, memCritical, diskWarn, diskCritical
        case procsWarn, procsCritical, zombiesWarn, zombiesCritical
        case health
    }

    init(
        cpuWarn: Double, cpuCritical: Double,
        memWarn: Double, memCritical: Double,
        diskWarn: Double, diskCritical: Double,
        procsWarn: Int = defaultProcsWarn,
        procsCritical: Int = defaultProcsCritical,
        zombiesWarn: Int = defaultZombiesWarn,
        zombiesCritical: Int = defaultZombiesCritical,
        health: HealthLimits = .defaults
    ) {
        self.cpuWarn = cpuWarn
        self.cpuCritical = cpuCritical
        self.memWarn = memWarn
        self.memCritical = memCritical
        self.diskWarn = diskWarn
        self.diskCritical = diskCritical
        self.procsWarn = max(1, procsWarn)
        self.procsCritical = max(self.procsWarn, procsCritical)
        self.zombiesWarn = max(1, zombiesWarn)
        self.zombiesCritical = max(self.zombiesWarn, zombiesCritical)
        self.health = health
    }

    init(_ p: PersistedThresholds) {
        self.init(
            cpuWarn: p.cpuWarn, cpuCritical: p.cpuCritical,
            memWarn: p.memWarn, memCritical: p.memCritical,
            diskWarn: p.diskWarn, diskCritical: p.diskCritical,
            procsWarn: p.procsWarn, procsCritical: p.procsCritical,
            zombiesWarn: p.zombiesWarn, zombiesCritical: p.zombiesCritical,
            health: p.health
        )
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let cpuW = try c.decode(Double.self, forKey: .cpuWarn)
        let cpuC = try c.decode(Double.self, forKey: .cpuCritical)
        let memW = try c.decode(Double.self, forKey: .memWarn)
        let memC = try c.decode(Double.self, forKey: .memCritical)
        let diskW = try c.decode(Double.self, forKey: .diskWarn)
        let diskC = try c.decode(Double.self, forKey: .diskCritical)
        self.init(
            cpuWarn: cpuW, cpuCritical: cpuC,
            memWarn: memW, memCritical: memC,
            diskWarn: diskW, diskCritical: diskC,
            procsWarn: try c.decodeIfPresent(Int.self, forKey: .procsWarn) ?? Self.defaultProcsWarn,
            procsCritical: try c.decodeIfPresent(Int.self, forKey: .procsCritical) ?? Self.defaultProcsCritical,
            zombiesWarn: try c.decodeIfPresent(Int.self, forKey: .zombiesWarn) ?? Self.defaultZombiesWarn,
            zombiesCritical: try c.decodeIfPresent(Int.self, forKey: .zombiesCritical) ?? Self.defaultZombiesCritical,
            health: try c.decodeIfPresent(HealthLimits.self, forKey: .health) ?? .defaults
        )
    }

    /// Warn/critical pair for one metric. Counts are stored as Int and
    /// round on set.
    subscript(metric: ThresholdMetric) -> ThresholdPair {
        get {
            switch metric {
            case .cpu: return ThresholdPair(warn: cpuWarn, critical: cpuCritical)
            case .mem: return ThresholdPair(warn: memWarn, critical: memCritical)
            case .disk: return ThresholdPair(warn: diskWarn, critical: diskCritical)
            case .procs: return ThresholdPair(warn: Double(procsWarn), critical: Double(procsCritical))
            case .zombies: return ThresholdPair(warn: Double(zombiesWarn), critical: Double(zombiesCritical))
            case .pids: return health.pids
            case .files: return health.files
            case .inodes: return health.inodes
            case .memPressure: return health.memPressure
            case .ioPressure: return health.ioPressure
            }
        }
        set {
            switch metric {
            case .cpu: cpuWarn = newValue.warn; cpuCritical = newValue.critical
            case .mem: memWarn = newValue.warn; memCritical = newValue.critical
            case .disk: diskWarn = newValue.warn; diskCritical = newValue.critical
            case .procs:
                procsWarn = max(1, Int(newValue.warn.rounded()))
                procsCritical = max(procsWarn, Int(newValue.critical.rounded()))
            case .zombies:
                zombiesWarn = max(1, Int(newValue.warn.rounded()))
                zombiesCritical = max(zombiesWarn, Int(newValue.critical.rounded()))
            case .pids: health.pids = newValue
            case .files: health.files = newValue
            case .inodes: health.inodes = newValue
            case .memPressure: health.memPressure = newValue
            case .ioPressure: health.ioPressure = newValue
            }
        }
    }

    /// Effective thresholds for a host: each overridden metric replaces the
    /// global pair.
    static func effective(global: MetricThresholds, override: ThresholdOverrides?) -> MetricThresholds {
        override?.applied(to: global) ?? global
    }
}

/// Per-node threshold overrides. A nil metric uses the global value.
struct ThresholdOverrides: Codable, Sendable, Equatable {
    var cpu: ThresholdPair?
    var mem: ThresholdPair?
    var disk: ThresholdPair?
    var procs: ThresholdPair?
    var zombies: ThresholdPair?
    var pids: ThresholdPair?
    var files: ThresholdPair?
    var inodes: ThresholdPair?
    var memPressure: ThresholdPair?
    var ioPressure: ThresholdPair?

    init(cpu: ThresholdPair? = nil, mem: ThresholdPair? = nil, disk: ThresholdPair? = nil,
         procs: ThresholdPair? = nil, zombies: ThresholdPair? = nil,
         pids: ThresholdPair? = nil, files: ThresholdPair? = nil, inodes: ThresholdPair? = nil,
         memPressure: ThresholdPair? = nil, ioPressure: ThresholdPair? = nil) {
        self.cpu = cpu
        self.mem = mem
        self.disk = disk
        self.procs = procs
        self.zombies = zombies
        self.pids = pids
        self.files = files
        self.inodes = inodes
        self.memPressure = memPressure
        self.ioPressure = ioPressure
    }

    /// The old all-or-nothing override set CPU, memory and disk only.
    init(legacy t: MetricThresholds) {
        self.init(cpu: t[.cpu], mem: t[.mem], disk: t[.disk])
    }

    var isEmpty: Bool { ThresholdMetric.allCases.allSatisfy { self[$0] == nil } }

    var overridden: [ThresholdMetric] { ThresholdMetric.allCases.filter { self[$0] != nil } }

    subscript(metric: ThresholdMetric) -> ThresholdPair? {
        get {
            switch metric {
            case .cpu: return cpu
            case .mem: return mem
            case .disk: return disk
            case .procs: return procs
            case .zombies: return zombies
            case .pids: return pids
            case .files: return files
            case .inodes: return inodes
            case .memPressure: return memPressure
            case .ioPressure: return ioPressure
            }
        }
        set {
            switch metric {
            case .cpu: cpu = newValue
            case .mem: mem = newValue
            case .disk: disk = newValue
            case .procs: procs = newValue
            case .zombies: zombies = newValue
            case .pids: pids = newValue
            case .files: files = newValue
            case .inodes: inodes = newValue
            case .memPressure: memPressure = newValue
            case .ioPressure: ioPressure = newValue
            }
        }
    }

    func applied(to global: MetricThresholds) -> MetricThresholds {
        var t = global
        for m in ThresholdMetric.allCases {
            if let pair = self[m] { t[m] = pair }
        }
        return t
    }
}
