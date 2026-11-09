import Foundation

/// Host health evaluated from one sample's `health` block and disk inodes.
/// All limits come from `MetricThresholds`.
struct HealthStatus: Sendable, Equatable {
    enum Signal: Sendable, Equatable {
        case procs, zombies, pids, files, memPressure, ioPressure, inodes, longProc
    }

    struct Reason: Sendable, Equatable {
        let signal: Signal
        let tint: ThresholdTint
        let text: String
    }

    /// Critical reasons first, then warn.
    let reasons: [Reason]
    let info: HealthInfo?

    static let empty = HealthStatus(reasons: [], info: nil)

    var tint: ThresholdTint {
        if reasons.contains(where: { $0.tint == .critical }) { return .critical }
        if reasons.contains(where: { $0.tint == .warn }) { return .warn }
        return .nominal
    }

    /// Tint for the health sustain window. Inodes alert at once like disk
    /// space, and long processes have their own sustain time. Muted signals
    /// do not count.
    func alertTint(muted: MutedSignals) -> ThresholdTint {
        let rest = reasons(unmuted: muted).filter { $0.signal != .inodes && $0.signal != .longProc }
        if rest.contains(where: { $0.tint == .critical }) { return .critical }
        if rest.contains(where: { $0.tint == .warn }) { return .warn }
        return .nominal
    }

    init(reasons: [Reason], info: HealthInfo?) {
        self.reasons = reasons
        self.info = info
    }

    func reasons(unmuted muted: MutedSignals) -> [Reason] {
        reasons.filter { !muted.mutes($0.signal) }
    }

    func alertTint(of signal: Signal, muted: MutedSignals) -> ThresholdTint {
        muted.mutes(signal) ? .nominal : tint(of: signal)
    }

    /// `kernelCriticalOnly`: with no pressure number, only the kernel
    /// critical level alerts. Use it when the host overrides memory pressure.
    init(
        info: HealthInfo?, disks: [DiskSample]?, thresholds t: MetricThresholds,
        longProcs: [LongProc] = [], now: Date = Date(), kernelCriticalOnly: Bool = false
    ) {
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
                add(.pids, level(f, warn: t.health.pids.warn, critical: t.health.pids.critical),
                    "PIDs \(Self.pct(f)) of limit")
            }
            if let f = Self.fraction(h.filesUsed, h.filesMax) {
                add(.files, level(f, warn: t.health.files.warn, critical: t.health.files.critical),
                    "Open files \(Self.pct(f)) of limit")
            }
            if let pct = h.memPressurePct {
                let mac = t.health.macMemPressure
                add(.memPressure, level(Double(pct), warn: mac.warn, critical: mac.critical),
                    "Memory pressure \(pct)%")
            } else if let p = h.psi {
                if p.memFull >= t.health.memPressure.critical {
                    add(.memPressure, .critical, "Memory pressure \(Int(p.memFull.rounded()))%")
                } else if p.memSome >= t.health.memPressure.warn {
                    add(.memPressure, .warn, "Memory pressure \(Int(p.memSome.rounded()))%")
                }
            } else {
                // A sampler older than 1.3.0 sends only the kernel level.
                switch h.memPressure {
                case 4: add(.memPressure, .critical, "Memory pressure critical")
                case 2 where !kernelCriticalOnly: add(.memPressure, .warn, "Memory pressure high")
                default: break
                }
            }
            if let p = h.psi {
                add(.ioPressure, level(p.ioFull, warn: t.health.ioPressure.warn, critical: t.health.ioPressure.critical),
                    "I/O pressure \(Int(p.ioFull.rounded()))%")
            }
        }
        for d in disks ?? [] {
            if let f = Self.fraction(d.inodesUsed, d.inodesTotal) {
                add(.inodes, level(f, warn: t.health.inodes.warn, critical: t.health.inodes.critical),
                    "Inodes \(Self.pct(f)) on \(d.mount)")
            }
        }

        for p in longProcs {
            add(.longProc, .warn, Self.longProcText(p, now: now))
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

    /// "OrbStack Helper 85% for 3h".
    static func longProcText(_ p: LongProc, now: Date) -> String {
        "\(p.shortName) \(Int(p.cpuPct.rounded()))% for \(ShortDuration.text(p.highFor(at: now)))"
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

extension HealthStatus.Signal {
    /// The threshold metric of the signal. `.longProc` has none.
    var metric: ThresholdMetric? {
        switch self {
        case .procs: return .procs
        case .zombies: return .zombies
        case .pids: return .pids
        case .files: return .files
        case .memPressure: return .memPressure
        case .ioPressure: return .ioPressure
        case .inodes: return .inodes
        case .longProc: return nil
        }
    }
}
