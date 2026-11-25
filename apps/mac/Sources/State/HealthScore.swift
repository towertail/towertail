import Foundation

/// Host health as one number. The score is the worst check.
/// Bands: 100 to 81 below warn, 80 to 41 from warn to critical,
/// 40 to 0 at critical or above.
struct HealthScore: Sendable, Equatable {
    enum Check: Hashable, Sendable {
        case signal(HealthStatus.Signal)
        case cpu, mem, disk
    }

    struct Part: Sendable, Equatable {
        let check: Check
        let value: Double
        let score: Double
        let tint: ThresholdTint
        /// Warn and critical of the check. Nil for a check with no limits.
        var limit: ThresholdPair? = nil

        var label: String { check.label }
    }

    /// Worst first.
    let parts: [Part]

    static let longProcScore = 70.0

    var score: Double { parts.first?.score ?? 100 }
    var worst: Part? { parts.first }
    var tint: ThresholdTint { Self.tint(score: score) }

    init(parts: [Part]) {
        self.parts = parts.sorted { $0.score < $1.score }
    }

    /// `cpu`, `mem` and `disk` are ratios. `inodeRatio` counts only when
    /// `disks` is nil, as in `HealthStatus`. Muted checks are skipped.
    /// `kernelCriticalOnly` has the same meaning as in `HealthStatus`.
    init(
        info: HealthInfo?, disks: [DiskSample]?, cpu: Double?, mem: Double?, disk: Double?,
        longProcs: [LongProc], thresholds t: MetricThresholds, muted: MutedSignals,
        inodeRatio: Double? = nil, kernelCriticalOnly: Bool = false
    ) {
        var out: [Part] = []
        func add(_ check: Check, _ value: Double, _ pair: ThresholdPair?, score: Double? = nil) {
            let s = score ?? pair.map { Self.score(value: value, warn: $0.warn, critical: $0.critical) } ?? 100
            out.append(Part(check: check, value: value, score: s, tint: Self.tint(score: s), limit: pair))
        }
        func signal(_ s: HealthStatus.Signal, _ value: Double, _ pair: ThresholdPair, score: Double? = nil) {
            if !muted.mutes(s) { add(.signal(s), value, pair, score: score) }
        }

        if let cpu, !muted.mutes(Metric.cpu) { add(.cpu, cpu, t[.cpu]) }
        if let mem, !muted.mutes(Metric.mem) { add(.mem, mem, t[.mem]) }
        if let disk, !muted.mutes(Metric.disk) { add(.disk, disk, t[.disk]) }

        if let h = info {
            signal(.procs, Double(h.procs), t[.procs])
            if let z = h.zombies { signal(.zombies, Double(z), t[.zombies]) }
            if let f = HealthStatus.fraction(h.pidsUsed, h.pidsMax) { signal(.pids, f, t[.pids]) }
            if let f = HealthStatus.fraction(h.filesUsed, h.filesMax) { signal(.files, f, t[.files]) }
            if let pct = h.memPressurePct {
                signal(.memPressure, Double(pct), t.health.macMemPressure)
            } else if let p = h.psi {
                let pair = t.health.memPressure
                signal(.memPressure, p.memSome, pair, score: Self.psiScore(some: p.memSome, full: p.memFull, pair: pair))
            } else if let level = h.memPressure {
                // A sampler older than 1.3.0 sends only the kernel level.
                let s: Double = level == 4 ? 20 : level == 2 && !kernelCriticalOnly ? 60 : 100
                signal(.memPressure, Double(level), t.health.memPressure, score: s)
            }
            if let p = h.psi { signal(.ioPressure, p.ioFull, t.health.ioPressure) }
        }
        let inode = (disks ?? []).compactMap { HealthStatus.fraction($0.inodesUsed, $0.inodesTotal) }.max()
            ?? (disks == nil ? inodeRatio : nil)
        if let inode { signal(.inodes, inode, t.health.inodes) }
        if !longProcs.isEmpty, !muted.mutes(.longProc) {
            add(.signal(.longProc), Double(longProcs.count), nil, score: Self.longProcScore)
        }
        self.init(parts: out)
    }

    static func score(value: Double, warn: Double, critical: Double) -> Double {
        if value >= critical { return max(0, 40 - 40 * (value - critical) / max(critical, .leastNonzeroMagnitude)) }
        if value >= warn { return 80 - 39 * (value - warn) / max(critical - warn, .leastNonzeroMagnitude) }
        return 100 - 19 * (value / max(warn, .leastNonzeroMagnitude))
    }

    static func tint(score: Double) -> ThresholdTint {
        score >= 81 ? .nominal : score >= 41 ? .warn : .critical
    }

    /// Linux memory pressure goes critical on "full" and warns on "some".
    static func psiScore(some: Double, full: Double, pair: ThresholdPair) -> Double {
        if full >= pair.critical { return score(value: full, warn: pair.critical, critical: pair.critical) }
        let s = score(value: some, warn: pair.warn, critical: pair.critical)
        return some >= pair.warn ? max(41, s) : s
    }
}

extension HealthScore.Check {
    var label: String {
        switch self {
        case .cpu: return "CPU"
        case .mem: return "Memory"
        case .disk: return "Disk"
        case .signal(let s):
            switch s {
            case .procs: return ThresholdMetric.procs.title
            case .zombies: return ThresholdMetric.zombies.title
            case .pids: return ThresholdMetric.pids.title
            case .files: return ThresholdMetric.files.title
            case .memPressure: return ThresholdMetric.memPressure.title
            case .ioPressure: return ThresholdMetric.ioPressure.title
            case .inodes: return ThresholdMetric.inodes.title
            case .longProc: return "Long-running process"
            }
        }
    }
}
