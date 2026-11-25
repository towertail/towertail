import Foundation

// The rules model of the server (docs/wire.md: HostRules, OrgRules,
// EffectiveRules, RulesView, RulesImport).

/// The client that set rules last. The server sets `userId` and `at`.
struct RemoteRulesOrigin: Codable, Sendable, Equatable {
    var client: String
    var device: String?
    var userId: UUID?
    var at: Date?

    init(client: String, device: String? = nil, userId: UUID? = nil, at: Date? = nil) {
        self.client = client
        self.device = device
        self.userId = userId
        self.at = at
    }

    enum CodingKeys: String, CodingKey { case client, device, userId, at }

    /// The time has fractional seconds, which `.iso8601` rejects. A bad
    /// time never fails the decode of a whole node.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        client = (try? c.decode(String.self, forKey: .client)) ?? "api"
        device = try? c.decodeIfPresent(String.self, forKey: .device)
        userId = try? c.decodeIfPresent(UUID.self, forKey: .userId)
        at = (try? c.decodeIfPresent(String.self, forKey: .at)).flatMap { $0 }.flatMap(SampleCodec.parseRFC3339UTC)
    }

    /// The server takes only `client` and `device` from a request.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(client, forKey: .client)
        try c.encodeIfPresent(device, forKey: .device)
    }

    /// The device that set the rules, or the client name.
    var source: String { device ?? client }
}

/// The alert timing that a host replaces. A nil field uses the org rule.
struct RemoteAlertOverrides: Codable, Sendable, Equatable {
    var cpu: AlertRule?
    var mem: AlertRule?
    var disk: AlertRule?
    var health: AlertRule?
    var tolerance: Double?

    init(cpu: AlertRule? = nil, mem: AlertRule? = nil, disk: AlertRule? = nil,
         health: AlertRule? = nil, tolerance: Double? = nil) {
        self.cpu = cpu
        self.mem = mem
        self.disk = disk
        self.health = health
        self.tolerance = tolerance
    }

    var isEmpty: Bool { self == RemoteAlertOverrides() }

    func applied(to global: AlertRules) -> AlertRules {
        var r = global
        if let cpu { r.cpu = cpu }
        if let mem { r.mem = mem }
        if let disk { r.disk = disk }
        if let health { r.health = health }
        if let tolerance { r = AlertRules(cpu: r.cpu, mem: r.mem, disk: r.disk, health: r.health, tolerance: tolerance) }
        return r
    }

    /// Only the rules of `host` that differ from `global`.
    static func diff(_ host: AlertRules, from global: AlertRules) -> RemoteAlertOverrides {
        RemoteAlertOverrides(
            cpu: host.cpu == global.cpu ? nil : host.cpu,
            mem: host.mem == global.mem ? nil : host.mem,
            disk: host.disk == global.disk ? nil : host.disk,
            health: host.health == global.health ? nil : host.health,
            tolerance: host.tolerance == global.tolerance ? nil : host.tolerance
        )
    }
}

struct RemoteOrgRules: Codable, Sendable, Equatable {
    var thresholds: MetricThresholds
    var alerts: AlertRules
    var muted: MutedSignals
    var longProc: LongProcRule
    var origin: RemoteRulesOrigin?

    init(thresholds: MetricThresholds = .defaults, alerts: AlertRules = .defaults,
         muted: MutedSignals = .empty, longProc: LongProcRule = .defaults, origin: RemoteRulesOrigin? = nil) {
        self.thresholds = thresholds
        self.alerts = alerts
        self.muted = muted
        self.longProc = longProc
        self.origin = origin
    }

    enum CodingKeys: String, CodingKey { case thresholds, alerts, muted, longProc, origin }

    /// A missing key has the default value, as on the server.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        thresholds = try c.decodeIfPresent(MetricThresholds.self, forKey: .thresholds) ?? .defaults
        alerts = try c.decodeIfPresent(AlertRules.self, forKey: .alerts) ?? .defaults
        muted = (try? c.decodeIfPresent(MutedSignals.self, forKey: .muted)) ?? .empty
        longProc = try c.decodeIfPresent(LongProcRule.self, forKey: .longProc) ?? .defaults
        origin = try? c.decodeIfPresent(RemoteRulesOrigin.self, forKey: .origin)
    }
}

/// The rules that one host sets. A nil field uses the org rule.
struct RemoteHostRules: Codable, Sendable, Equatable {
    var thresholds: ThresholdOverrides?
    var alerts: RemoteAlertOverrides?
    var muted: MutedSignals
    var longProc: LongProcRule?
    var ignoredProcs: [String]
    var origin: RemoteRulesOrigin?

    init(thresholds: ThresholdOverrides? = nil, alerts: RemoteAlertOverrides? = nil, muted: MutedSignals = .empty,
         longProc: LongProcRule? = nil, ignoredProcs: [String] = [], origin: RemoteRulesOrigin? = nil) {
        self.thresholds = thresholds?.isEmpty == true ? nil : thresholds
        self.alerts = alerts?.isEmpty == true ? nil : alerts
        self.muted = muted
        self.longProc = longProc
        self.ignoredProcs = ignoredProcs
        self.origin = origin
    }

    enum CodingKeys: String, CodingKey { case thresholds, alerts, muted, longProc, ignoredProcs, origin }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(
            thresholds: try c.decodeIfPresent(ThresholdOverrides.self, forKey: .thresholds),
            alerts: try c.decodeIfPresent(RemoteAlertOverrides.self, forKey: .alerts),
            muted: (try? c.decodeIfPresent(MutedSignals.self, forKey: .muted)) ?? .empty,
            longProc: try? c.decodeIfPresent(LongProcRule.self, forKey: .longProc),
            ignoredProcs: (try? c.decodeIfPresent([String].self, forKey: .ignoredProcs)) ?? [],
            origin: try? c.decodeIfPresent(RemoteRulesOrigin.self, forKey: .origin)
        )
    }

    /// A body must always carry `muted`. The server clears an override
    /// that the body leaves out, so a nil field is simply not sent.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(thresholds, forKey: .thresholds)
        try c.encodeIfPresent(alerts, forKey: .alerts)
        try c.encode(muted, forKey: .muted)
        try c.encodeIfPresent(longProc, forKey: .longProc)
        if !ignoredProcs.isEmpty { try c.encode(ignoredProcs, forKey: .ignoredProcs) }
        try c.encodeIfPresent(origin, forKey: .origin)
    }
}

/// The rules that the engine applies to one host.
struct RemoteEffectiveRules: Codable, Sendable, Equatable {
    /// `org` or `host` for each rule.
    struct Sources: Codable, Sendable, Equatable {
        var thresholds: [String: String]
        var alerts: [String: String]
        var muted: [String: String]
        var longProc: String

        init(thresholds: [String: String] = [:], alerts: [String: String] = [:],
             muted: [String: String] = [:], longProc: String = "org") {
            self.thresholds = thresholds
            self.alerts = alerts
            self.muted = muted
            self.longProc = longProc
        }

        enum CodingKeys: String, CodingKey { case thresholds, alerts, muted, longProc }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            thresholds = (try? c.decodeIfPresent([String: String].self, forKey: .thresholds)) ?? [:]
            alerts = (try? c.decodeIfPresent([String: String].self, forKey: .alerts)) ?? [:]
            muted = (try? c.decodeIfPresent([String: String].self, forKey: .muted)) ?? [:]
            longProc = (try? c.decodeIfPresent(String.self, forKey: .longProc)) ?? "org"
        }
    }

    var thresholds: MetricThresholds
    var alerts: AlertRules
    var muted: MutedSignals
    var longProc: LongProcRule
    var ignore: [String]
    var sources: Sources

    init(thresholds: MetricThresholds = .defaults, alerts: AlertRules = .defaults, muted: MutedSignals = .empty,
         longProc: LongProcRule = .defaults, ignore: [String] = [], sources: Sources = Sources()) {
        self.thresholds = thresholds
        self.alerts = alerts
        self.muted = muted
        self.longProc = longProc
        self.ignore = ignore
        self.sources = sources
    }

    enum CodingKeys: String, CodingKey { case thresholds, alerts, muted, longProc, ignore, sources }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(
            thresholds: try c.decodeIfPresent(MetricThresholds.self, forKey: .thresholds) ?? .defaults,
            alerts: try c.decodeIfPresent(AlertRules.self, forKey: .alerts) ?? .defaults,
            muted: (try? c.decodeIfPresent(MutedSignals.self, forKey: .muted)) ?? .empty,
            longProc: try c.decodeIfPresent(LongProcRule.self, forKey: .longProc) ?? .defaults,
            ignore: (try? c.decodeIfPresent([String].self, forKey: .ignore)) ?? [],
            sources: try c.decodeIfPresent(Sources.self, forKey: .sources) ?? Sources()
        )
    }

    /// True when the host sets this metric itself.
    func isHostPair(_ metric: ThresholdMetric) -> Bool { sources.thresholds[metric.rawValue] == "host" }
}

/// `GET /v1/rules`: the org rules and the rules of each host.
struct RemoteRulesView: Codable, Sendable, Equatable {
    struct Host: Codable, Sendable, Equatable {
        var nodeId: UUID
        var host: RemoteHostRules
        var effective: RemoteEffectiveRules
    }

    var org: RemoteOrgRules
    var hosts: [Host]
}

/// `POST /v1/rules/import`. The server replaces the org rules (when `org`
/// is set) and the rules of the named hosts at once.
struct RemoteRulesImport: Encodable, Sendable, Equatable {
    struct HostPart: Encodable, Sendable, Equatable {
        var nodeId: UUID
        var rules: RemoteHostRules
    }

    var org: RemoteOrgRules?
    var hosts: [HostPart]
    var client: String
    var device: String
}

struct RemoteRulesImportResult: Decodable, Sendable, Equatable {
    var backupId: UUID
    var hosts: Int
    var org: Bool
}
