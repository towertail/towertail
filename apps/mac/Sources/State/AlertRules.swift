import Foundation

/// Which severities of a metric may notify.
enum AlertNotify: String, Codable, Sendable, CaseIterable {
    case off, critical, all

    func allows(_ tint: ThresholdTint) -> Bool {
        switch (self, tint) {
        case (.all, .warn), (.all, .critical), (.critical, .critical): return true
        default: return false
        }
    }
}

/// How long a metric must stay over its threshold before it alerts, and
/// which severities notify. Card tints ignore this; it gates notifications
/// and the menu-bar icon only.
struct AlertRule: Codable, Sendable, Equatable {
    var sustainSeconds: Int
    var notify: AlertNotify

    static let maxSustainSeconds = 3600

    init(sustainSeconds: Int, notify: AlertNotify) {
        self.sustainSeconds = min(max(0, sustainSeconds), Self.maxSustainSeconds)
        self.notify = notify
    }

    enum CodingKeys: String, CodingKey { case sustainSeconds, notify }

    var logValue: String { "\(sustainSeconds)s/\(notify.rawValue)" }

    /// Decodes with per-field fallback so a partial object keeps the other default.
    static func decode(from c: KeyedDecodingContainer<AlertRules.CodingKeys>,
                       key: AlertRules.CodingKeys, fallback: AlertRule) throws -> AlertRule {
        guard c.contains(key) else { return fallback }
        let r = try c.nestedContainer(keyedBy: CodingKeys.self, forKey: key)
        return AlertRule(
            sustainSeconds: try r.decodeIfPresent(Int.self, forKey: .sustainSeconds) ?? fallback.sustainSeconds,
            notify: (try? r.decodeIfPresent(AlertNotify.self, forKey: .notify)) ?? fallback.notify
        )
    }
}

/// Per-metric alert rules. Stored globally under `thresholds.alerts` and
/// per node as `customAlerts`.
struct AlertRules: Codable, Sendable, Equatable {
    var cpu: AlertRule
    var mem: AlertRule
    var disk: AlertRule
    var health: AlertRule
    /// Fraction of the sustain window that must be over the line. Below 1,
    /// a short dip does not restart the timer.
    var tolerance: Double

    static let toleranceRange = 0.5...1.0

    static let defaults = AlertRules(
        cpu: AlertRule(sustainSeconds: 300, notify: .critical),
        mem: AlertRule(sustainSeconds: 120, notify: .all),
        disk: AlertRule(sustainSeconds: 0, notify: .all),
        health: AlertRule(sustainSeconds: 300, notify: .all),
        tolerance: 0.8
    )

    init(cpu: AlertRule, mem: AlertRule, disk: AlertRule, health: AlertRule, tolerance: Double) {
        self.cpu = cpu
        self.mem = mem
        self.disk = disk
        self.health = health
        self.tolerance = min(max(tolerance, Self.toleranceRange.lowerBound), Self.toleranceRange.upperBound)
    }

    enum CodingKeys: String, CodingKey { case cpu, mem, disk, health, tolerance }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = Self.defaults
        self.init(
            cpu: try AlertRule.decode(from: c, key: .cpu, fallback: d.cpu),
            mem: try AlertRule.decode(from: c, key: .mem, fallback: d.mem),
            disk: try AlertRule.decode(from: c, key: .disk, fallback: d.disk),
            health: try AlertRule.decode(from: c, key: .health, fallback: d.health),
            tolerance: try c.decodeIfPresent(Double.self, forKey: .tolerance) ?? d.tolerance
        )
    }

    /// Builds rules from the old sample-count sustain settings. A count of 1
    /// was the old "immediate" default, so it takes the new default instead.
    static func migrated(cpuSamples: Int, memSamples: Int, diskSamples: Int, pollSeconds: Int) -> AlertRules {
        var r = defaults
        let poll = max(1, pollSeconds)
        if cpuSamples > 1 { r.cpu.sustainSeconds = min(cpuSamples * poll, AlertRule.maxSustainSeconds) }
        if memSamples > 1 { r.mem.sustainSeconds = min(memSamples * poll, AlertRule.maxSustainSeconds) }
        if diskSamples > 1 { r.disk.sustainSeconds = min(diskSamples * poll, AlertRule.maxSustainSeconds) }
        return r
    }

    subscript(metric: Metric) -> AlertRule {
        get {
            switch metric {
            case .cpu: return cpu
            case .mem: return mem
            case .disk: return disk
            case .health: return health
            case .net: return AlertRule(sustainSeconds: 0, notify: .off)
            }
        }
        set {
            switch metric {
            case .cpu: cpu = newValue
            case .mem: mem = newValue
            case .disk: disk = newValue
            case .health: health = newValue
            case .net: break
            }
        }
    }
}

extension ThresholdTint {
    /// Severity order: stale and nominal rank 0.
    var rank: Int {
        switch self {
        case .nominal, .stale: return 0
        case .warn: return 1
        case .critical: return 2
        }
    }
}

/// Recent per-sample levels of one metric. Each sample holds its level until
/// the next sample (sample-and-hold), so the level is time-weighted and an
/// uneven poll does not skew it.
struct SustainWindow: Sendable {
    private struct Point: Sendable {
        let t: Date
        let level: ThresholdTint
    }

    private var points: [Point] = []

    var latest: ThresholdTint { points.last?.level ?? .nominal }

    /// Adds a sample and drops points older than `keep`, except one anchor
    /// before the cutoff so the window start stays covered.
    mutating func record(_ level: ThresholdTint, at t: Date, keep seconds: Int) {
        if let last = points.last, t < last.t { points.removeAll() }
        points.append(Point(t: t, level: level))
        let cutoff = t.addingTimeInterval(-Double(seconds))
        while points.count > 1, points[1].t <= cutoff { points.removeFirst() }
    }

    mutating func reset() { points.removeAll() }

    /// Sustained level over the last `seconds`. Nominal until the window
    /// has full history.
    func level(sustain seconds: Int, tolerance: Double) -> ThresholdTint {
        guard let now = points.last?.t else { return .nominal }
        guard seconds > 0 else { return latest }
        let span = Double(seconds)
        let start = now.addingTimeInterval(-span)
        guard let first = points.first, first.t <= start else { return .nominal }

        var warnTime = 0.0
        var criticalTime = 0.0
        for (a, b) in zip(points, points.dropFirst()) {
            let d = b.t.timeIntervalSince(max(a.t, start))
            guard d > 0 else { continue }
            if a.level.rank >= ThresholdTint.warn.rank { warnTime += d }
            if a.level == .critical { criticalTime += d }
        }
        if criticalTime / span >= tolerance { return .critical }
        if warnTime / span >= tolerance { return .warn }
        return .nominal
    }
}

/// Finds one process that uses a lot of CPU for a long time. `cpuPct`
/// uses the `top` convention: 100 is one full core. Stored globally as
/// `longProc` and per node as `customLongProc`.
struct LongProcRule: Codable, Sendable, Equatable {
    var enabled: Bool
    var cpuPct: Double
    var sustainSeconds: Int
    /// Only `off` and `all` apply: a long process is a warning.
    var notify: AlertNotify
    /// Process names, matched exactly without case.
    var ignore: [String]

    /// Fraction of the window that must be high. Fixed in all products.
    static let tolerance = 0.8
    static let cpuRange = 10.0...800.0
    static let sustainRange = 60...86_400

    static let defaults = LongProcRule(enabled: true, cpuPct: 50, sustainSeconds: 1800, notify: .all, ignore: [])

    init(enabled: Bool, cpuPct: Double, sustainSeconds: Int, notify: AlertNotify, ignore: [String]) {
        self.enabled = enabled
        self.cpuPct = min(max(cpuPct, Self.cpuRange.lowerBound), Self.cpuRange.upperBound)
        self.sustainSeconds = min(max(sustainSeconds, Self.sustainRange.lowerBound), Self.sustainRange.upperBound)
        self.notify = notify
        self.ignore = ignore
    }

    enum CodingKeys: String, CodingKey { case enabled, cpuPct, sustainSeconds, notify, ignore }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = Self.defaults
        self.init(
            enabled: (try? c.decodeIfPresent(Bool.self, forKey: .enabled)) ?? d.enabled,
            cpuPct: (try? c.decodeIfPresent(Double.self, forKey: .cpuPct)) ?? d.cpuPct,
            sustainSeconds: (try? c.decodeIfPresent(Int.self, forKey: .sustainSeconds)) ?? d.sustainSeconds,
            notify: (try? c.decodeIfPresent(AlertNotify.self, forKey: .notify)) ?? d.notify,
            ignore: (try? c.decodeIfPresent([String].self, forKey: .ignore)) ?? d.ignore
        )
    }

    var notifies: Bool { notify != .off }

    var logValue: String {
        enabled ? "\(Int(cpuPct))%/\(sustainSeconds)s/\(notify.rawValue)" : "off"
    }

    /// Lowercased union of the rule list and the other lists.
    func ignoreSet(adding other: [String]...) -> Set<String> {
        Set((ignore + other.joined()).map { $0.lowercased() })
    }
}
