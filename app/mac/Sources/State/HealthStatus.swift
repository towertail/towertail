import Foundation

/// Host health evaluated from one sample's `health` block and disk inodes.
/// Process and zombie limits come from `MetricThresholds`; the other
/// limits are fixed defaults below.
struct HealthStatus: Sendable, Equatable {
    enum Signal: Sendable, Equatable {
        case procs, zombies, pids, files, memPressure, ioPressure, inodes
    }

    struct Reason: Sendable, Equatable {
        let signal: Signal
        let tint: ThresholdTint
        let text: String
    }

    static let pidsWarn = 0.70
    static let pidsCritical = 0.90
    static let filesWarn = 0.80
    static let filesCritical = 0.95
    static let inodesWarn = 0.85
    static let inodesCritical = 0.95
    /// PSI avg10 percent. Memory warns on `some`, goes critical on `full`.
    static let memSomeWarn = 10.0
    static let memFullCritical = 20.0
    static let ioFullWarn = 20.0
    static let ioFullCritical = 40.0

    /// Critical reasons first, then warn.
    let reasons: [Reason]
    let info: HealthInfo?

    static let empty = HealthStatus(reasons: [], info: nil)

    var tint: ThresholdTint {
        if reasons.contains(where: { $0.tint == .critical }) { return .critical }
        if reasons.contains(where: { $0.tint == .warn }) { return .warn }
        return .nominal
    }

    /// Inodes alert at once like disk space, so the sustain rule skips them.
    var tintExcludingInodes: ThresholdTint {
        let rest = reasons.filter { $0.signal != .inodes }
        if rest.contains(where: { $0.tint == .critical }) { return .critical }
        if rest.contains(where: { $0.tint == .warn }) { return .warn }
        return .nominal
    }

    init(reasons: [Reason], info: HealthInfo?) {
        self.reasons = reasons
        self.info = info
    }

    init(info: HealthInfo?, disks: [DiskSample]?, thresholds t: MetricThresholds) {
        var out: [Reason] = []
        func add(_ signal: Signal, _ tint: ThresholdTint, _ text: String) {
            if tint != .nominal { out.append(Reason(signal: signal, tint: tint, text: text)) }
        }
        func level(_ v: Double, warn: Double, critical: Double) -> ThresholdTint {
            v >= critical ? .critical : v >= warn ? .warn : .nominal
        }

        if let h = info {
            add(.procs, level(Double(h.procs), warn: Double(t.procsWarn), critical: Double(t.procsCritical)),
                "\(Self.count(h.procs)) processes")
            if let z = h.zombies {
                let top = h.zombieParents?.first.map { " (\($0.name))" } ?? ""
                add(.zombies, level(Double(z), warn: Double(t.zombiesWarn), critical: Double(t.zombiesCritical)),
                    "\(Self.count(z)) zombies\(top)")
            }
            if let f = Self.fraction(h.pidsUsed, h.pidsMax) {
                add(.pids, level(f, warn: Self.pidsWarn, critical: Self.pidsCritical),
                    "PIDs \(Self.pct(f)) of limit")
            }
            if let f = Self.fraction(h.filesUsed, h.filesMax) {
                add(.files, level(f, warn: Self.filesWarn, critical: Self.filesCritical),
                    "Open files \(Self.pct(f)) of limit")
            }
            if let p = h.psi {
                if p.memFull >= Self.memFullCritical {
                    add(.memPressure, .critical, "Memory pressure \(Int(p.memFull.rounded()))%")
                } else if p.memSome >= Self.memSomeWarn {
                    add(.memPressure, .warn, "Memory pressure \(Int(p.memSome.rounded()))%")
                }
                add(.ioPressure, level(p.ioFull, warn: Self.ioFullWarn, critical: Self.ioFullCritical),
                    "I/O pressure \(Int(p.ioFull.rounded()))%")
            }
            switch h.memPressure {
            case 4: add(.memPressure, .critical, "Memory pressure critical")
            case 2: add(.memPressure, .warn, "Memory pressure high")
            default: break
            }
        }
        for d in disks ?? [] {
            if let f = Self.fraction(d.inodesUsed, d.inodesTotal) {
                add(.inodes, level(f, warn: Self.inodesWarn, critical: Self.inodesCritical),
                    "Inodes \(Self.pct(f)) on \(d.mount)")
            }
        }

        self.reasons = out.filter { $0.tint == .critical } + out.filter { $0.tint == .warn }
        self.info = info
    }

    /// Worst tint among reasons for one signal.
    func tint(of signal: Signal) -> ThresholdTint {
        reasons.first(where: { $0.signal == signal })?.tint ?? .nominal
    }

    /// First reason plus "+N more", for the compact card line.
    var summary: String? {
        guard let first = reasons.first else { return nil }
        return reasons.count > 1 ? "\(first.text) +\(reasons.count - 1) more" : first.text
    }

    static func fraction(_ used: Int64?, _ max: Int64?) -> Double? {
        guard let used, let max, max > 0 else { return nil }
        return Double(used) / Double(max)
    }

    static func count(_ n: Int) -> String {
        n.formatted(.number.grouping(.automatic))
    }

    private static func pct(_ f: Double) -> String {
        "\(Int((f * 100).rounded()))%"
    }
}
