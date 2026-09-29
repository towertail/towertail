import Foundation

struct PersistedSettings: Codable, Equatable {
    var nodes: [Node]
    var thresholds: PersistedThresholds
    var localPollingIntervalSeconds: Int
    var sshPollingIntervalSeconds: Int
    var cardDensity: String
    var notificationsEnabled: Bool
    var notifyWarn: Bool
    var notifyCritical: Bool
    var notifyDebounceSeconds: Int
    var launchAtLogin: Bool
    var autoUpdateSamplersEnabled: Bool
    var defaultTerminalApp: String
    var postWakeGraceSeconds: Int
    /// Platform-specific blocks preserved verbatim from the on-disk file so a
    /// cross-OS round trip never loses the other OS's settings. `platform.darwin`
    /// is our own; `platform.windows` / `platform.linux` come from the Windows
    /// (or future Linux) client and are written back untouched on save.
    var platformBlocks: PlatformBlocks

    static let defaults = PersistedSettings(
        nodes: [Node.localMac()],
        thresholds: .defaults,
        localPollingIntervalSeconds: 2,
        sshPollingIntervalSeconds: 10,
        cardDensity: "a",
        notificationsEnabled: false,
        notifyWarn: true,
        notifyCritical: true,
        notifyDebounceSeconds: 60,
        launchAtLogin: false,
        autoUpdateSamplersEnabled: false,
        defaultTerminalApp: "Terminal",
        postWakeGraceSeconds: 15,
        platformBlocks: .init(foreign: [:])
    )

    enum CodingKeys: String, CodingKey {
        case nodes, thresholds
        case localPollingIntervalSeconds, sshPollingIntervalSeconds
        case cardDensity
        case notificationsEnabled, notifyWarn, notifyCritical, notifyDebounceSeconds
        case launchAtLogin
        case autoUpdateSamplersEnabled
        case defaultTerminalApp
        case postWakeGraceSeconds
        case pollingIntervalSeconds // legacy single-value field
        case platform              // new envelope { darwin: {...}, windows: {...} }
        case schemaVersion
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.nodes = try c.decode([Node].self, forKey: .nodes)
        self.thresholds = try c.decode(PersistedThresholds.self, forKey: .thresholds)
        self.cardDensity = try c.decode(String.self, forKey: .cardDensity)
        self.notificationsEnabled = try c.decode(Bool.self, forKey: .notificationsEnabled)
        self.notifyWarn = try c.decode(Bool.self, forKey: .notifyWarn)
        self.notifyCritical = try c.decode(Bool.self, forKey: .notifyCritical)
        self.notifyDebounceSeconds = try c.decode(Int.self, forKey: .notifyDebounceSeconds)
        self.autoUpdateSamplersEnabled = try c.decodeIfPresent(Bool.self, forKey: .autoUpdateSamplersEnabled)
            ?? PersistedSettings.defaults.autoUpdateSamplersEnabled
        self.postWakeGraceSeconds = try c.decodeIfPresent(Int.self, forKey: .postWakeGraceSeconds)
            ?? PersistedSettings.defaults.postWakeGraceSeconds

        if let local = try c.decodeIfPresent(Int.self, forKey: .localPollingIntervalSeconds) {
            self.localPollingIntervalSeconds = local
        } else if let legacy = try c.decodeIfPresent(Int.self, forKey: .pollingIntervalSeconds) {
            self.localPollingIntervalSeconds = legacy
        } else {
            self.localPollingIntervalSeconds = PersistedSettings.defaults.localPollingIntervalSeconds
        }
        if let ssh = try c.decodeIfPresent(Int.self, forKey: .sshPollingIntervalSeconds) {
            self.sshPollingIntervalSeconds = ssh
        } else if let legacy = try c.decodeIfPresent(Int.self, forKey: .pollingIntervalSeconds) {
            self.sshPollingIntervalSeconds = legacy
        } else {
            self.sshPollingIntervalSeconds = PersistedSettings.defaults.sshPollingIntervalSeconds
        }

        // Platform envelope ------------------------------------------------
        // New shape: top-level "platform" object keyed by OS ("darwin" /
        // "windows" / "linux"), each carrying that OS's private settings.
        // Legacy shape: launchAtLogin/defaultTerminalApp at the root (pre-Windows
        // port). Read either; preserve unknown OS keys verbatim on save.
        var platform = PlatformBlocks(foreign: [:])

        let envelope = try c.decodeIfPresent([String: DarwinOrForeignBlock].self, forKey: .platform)

        if let envelope, let darwin = envelope["darwin"]?.asDarwin() {
            self.launchAtLogin = darwin.launchAtLogin ?? PersistedSettings.defaults.launchAtLogin
            self.defaultTerminalApp = darwin.defaultTerminalApp ?? PersistedSettings.defaults.defaultTerminalApp
        } else {
            // Legacy flat form — migrate into the envelope on next save.
            self.launchAtLogin = try c.decodeIfPresent(Bool.self, forKey: .launchAtLogin)
                ?? PersistedSettings.defaults.launchAtLogin
            self.defaultTerminalApp = try c.decodeIfPresent(String.self, forKey: .defaultTerminalApp)
                ?? PersistedSettings.defaults.defaultTerminalApp
        }

        if let envelope {
            for (key, block) in envelope where key != "darwin" {
                platform.foreign[key] = block.raw
            }
        }
        self.platformBlocks = platform
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(1, forKey: .schemaVersion)
        try c.encode(nodes, forKey: .nodes)
        try c.encode(thresholds, forKey: .thresholds)
        try c.encode(localPollingIntervalSeconds, forKey: .localPollingIntervalSeconds)
        try c.encode(sshPollingIntervalSeconds, forKey: .sshPollingIntervalSeconds)
        try c.encode(cardDensity, forKey: .cardDensity)
        try c.encode(notificationsEnabled, forKey: .notificationsEnabled)
        try c.encode(notifyWarn, forKey: .notifyWarn)
        try c.encode(notifyCritical, forKey: .notifyCritical)
        try c.encode(notifyDebounceSeconds, forKey: .notifyDebounceSeconds)
        try c.encode(autoUpdateSamplersEnabled, forKey: .autoUpdateSamplersEnabled)
        try c.encode(postWakeGraceSeconds, forKey: .postWakeGraceSeconds)
        // Legacy flat fields are still emitted so older Mac binaries keep
        // working if the user downgrades. They duplicate platform.darwin; the
        // loader prefers platform.darwin when both are present.
        try c.encode(launchAtLogin, forKey: .launchAtLogin)
        try c.encode(defaultTerminalApp, forKey: .defaultTerminalApp)

        // Normalized platform envelope.
        var envelope: [String: DarwinOrForeignBlock] = [:]
        envelope["darwin"] = .darwin(
            DarwinSettingsBlock(launchAtLogin: launchAtLogin, defaultTerminalApp: defaultTerminalApp)
        )
        for (key, raw) in platformBlocks.foreign {
            envelope[key] = .foreign(raw)
        }
        try c.encode(envelope, forKey: .platform)
    }

    init(
        nodes: [Node],
        thresholds: PersistedThresholds,
        localPollingIntervalSeconds: Int,
        sshPollingIntervalSeconds: Int,
        cardDensity: String,
        notificationsEnabled: Bool,
        notifyWarn: Bool,
        notifyCritical: Bool,
        notifyDebounceSeconds: Int,
        launchAtLogin: Bool,
        autoUpdateSamplersEnabled: Bool,
        defaultTerminalApp: String,
        postWakeGraceSeconds: Int,
        platformBlocks: PlatformBlocks = .init(foreign: [:])
    ) {
        self.nodes = nodes
        self.thresholds = thresholds
        self.localPollingIntervalSeconds = localPollingIntervalSeconds
        self.sshPollingIntervalSeconds = sshPollingIntervalSeconds
        self.cardDensity = cardDensity
        self.notificationsEnabled = notificationsEnabled
        self.notifyWarn = notifyWarn
        self.notifyCritical = notifyCritical
        self.notifyDebounceSeconds = notifyDebounceSeconds
        self.launchAtLogin = launchAtLogin
        self.autoUpdateSamplersEnabled = autoUpdateSamplersEnabled
        self.defaultTerminalApp = defaultTerminalApp
        self.postWakeGraceSeconds = postWakeGraceSeconds
        self.platformBlocks = platformBlocks
    }
}

/// Holds platform-specific settings blocks that are NOT native to this OS.
/// On macOS we round-trip `platform.windows` / `platform.linux` verbatim so a
/// user who edits the file from the Windows client doesn't lose their Windows
/// prefs the next time the Mac client saves.
struct PlatformBlocks: Equatable {
    /// Keyed by OS id ("windows", "linux"). darwin is kept in top-level fields.
    var foreign: [String: JSONValue]
}

struct DarwinSettingsBlock: Codable, Equatable {
    var launchAtLogin: Bool?
    var defaultTerminalApp: String?
}

/// Sum type for a single entry in the `platform` envelope. On decode we either
/// recognize the block as ours (`darwin`) or keep the raw JSON around so the
/// encoder can emit it back unmodified.
enum DarwinOrForeignBlock: Codable, Equatable {
    case darwin(DarwinSettingsBlock)
    case foreign(JSONValue)

    init(from decoder: Decoder) throws {
        let raw = try JSONValue(from: decoder)
        self = .foreign(raw)
    }

    func encode(to encoder: Encoder) throws {
        switch self {
        case .darwin(let block): try block.encode(to: encoder)
        case .foreign(let raw):  try raw.encode(to: encoder)
        }
    }

    var raw: JSONValue {
        switch self {
        case .darwin(let b):
            let dict: [String: JSONValue] = [
                "launchAtLogin": b.launchAtLogin.map { .bool($0) } ?? .null,
                "defaultTerminalApp": b.defaultTerminalApp.map { .string($0) } ?? .null,
            ]
            return .object(dict)
        case .foreign(let raw): return raw
        }
    }

    func asDarwin() -> DarwinSettingsBlock? {
        switch self {
        case .darwin(let b): return b
        case .foreign(let raw):
            guard case .object(let obj) = raw else { return nil }
            var launch: Bool? = nil
            var term: String? = nil
            if case .bool(let v) = obj["launchAtLogin"] { launch = v }
            if case .string(let v) = obj["defaultTerminalApp"] { term = v }
            return DarwinSettingsBlock(launchAtLogin: launch, defaultTerminalApp: term)
        }
    }
}

/// Minimal JSON value type for preserving foreign platform blocks across
/// load/save without a full-fat JSONSerialization round-trip. Equatable so
/// tests can diff the envelope.
indirect enum JSONValue: Codable, Equatable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self)   { self = .bool(b);   return }
        if let n = try? c.decode(Double.self) { self = .number(n); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        if let o = try? c.decode([String: JSONValue].self) { self = .object(o); return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "JSONValue")
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }
}

struct PersistedThresholds: Codable, Equatable {
    var cpuWarn: Double
    var cpuCritical: Double
    var memWarn: Double
    var memCritical: Double
    var diskWarn: Double
    var diskCritical: Double
    /// See MetricThresholds.cpuSustainSamples. Defaults to 1 (no sustain) so
    /// existing on-disk settings written before this field round-trip cleanly.
    var cpuSustainSamples: Int
    var memSustainSamples: Int
    var diskSustainSamples: Int
    /// See MetricThresholds.procsWarn. Missing keys load as defaults.
    var procsWarn: Int
    var procsCritical: Int
    var zombiesWarn: Int
    var zombiesCritical: Int

    static let defaults = PersistedThresholds(
        cpuWarn: 0.75, cpuCritical: 0.90,
        memWarn: 0.75, memCritical: 0.90,
        diskWarn: 0.85, diskCritical: 0.95,
        cpuSustainSamples: 1, memSustainSamples: 1, diskSustainSamples: 1
    )

    enum CodingKeys: String, CodingKey {
        case cpuWarn, cpuCritical, memWarn, memCritical, diskWarn, diskCritical
        case cpuSustainSamples, memSustainSamples, diskSustainSamples
        case procsWarn, procsCritical, zombiesWarn, zombiesCritical
    }

    init(
        cpuWarn: Double, cpuCritical: Double,
        memWarn: Double, memCritical: Double,
        diskWarn: Double, diskCritical: Double,
        cpuSustainSamples: Int = 1,
        memSustainSamples: Int = 1,
        diskSustainSamples: Int = 1,
        procsWarn: Int = MetricThresholds.defaultProcsWarn,
        procsCritical: Int = MetricThresholds.defaultProcsCritical,
        zombiesWarn: Int = MetricThresholds.defaultZombiesWarn,
        zombiesCritical: Int = MetricThresholds.defaultZombiesCritical
    ) {
        self.cpuWarn = cpuWarn
        self.cpuCritical = cpuCritical
        self.memWarn = memWarn
        self.memCritical = memCritical
        self.diskWarn = diskWarn
        self.diskCritical = diskCritical
        self.cpuSustainSamples = max(1, cpuSustainSamples)
        self.memSustainSamples = max(1, memSustainSamples)
        self.diskSustainSamples = max(1, diskSustainSamples)
        self.procsWarn = max(1, procsWarn)
        self.procsCritical = max(self.procsWarn, procsCritical)
        self.zombiesWarn = max(1, zombiesWarn)
        self.zombiesCritical = max(self.zombiesWarn, zombiesCritical)
    }

    init(_ t: MetricThresholds) {
        self.init(
            cpuWarn: t.cpuWarn, cpuCritical: t.cpuCritical,
            memWarn: t.memWarn, memCritical: t.memCritical,
            diskWarn: t.diskWarn, diskCritical: t.diskCritical,
            cpuSustainSamples: t.cpuSustainSamples,
            memSustainSamples: t.memSustainSamples,
            diskSustainSamples: t.diskSustainSamples,
            procsWarn: t.procsWarn, procsCritical: t.procsCritical,
            zombiesWarn: t.zombiesWarn, zombiesCritical: t.zombiesCritical
        )
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(
            cpuWarn: try c.decode(Double.self, forKey: .cpuWarn),
            cpuCritical: try c.decode(Double.self, forKey: .cpuCritical),
            memWarn: try c.decode(Double.self, forKey: .memWarn),
            memCritical: try c.decode(Double.self, forKey: .memCritical),
            diskWarn: try c.decode(Double.self, forKey: .diskWarn),
            diskCritical: try c.decode(Double.self, forKey: .diskCritical),
            cpuSustainSamples: try c.decodeIfPresent(Int.self, forKey: .cpuSustainSamples) ?? 1,
            memSustainSamples: try c.decodeIfPresent(Int.self, forKey: .memSustainSamples) ?? 1,
            diskSustainSamples: try c.decodeIfPresent(Int.self, forKey: .diskSustainSamples) ?? 1,
            procsWarn: try c.decodeIfPresent(Int.self, forKey: .procsWarn) ?? MetricThresholds.defaultProcsWarn,
            procsCritical: try c.decodeIfPresent(Int.self, forKey: .procsCritical) ?? MetricThresholds.defaultProcsCritical,
            zombiesWarn: try c.decodeIfPresent(Int.self, forKey: .zombiesWarn) ?? MetricThresholds.defaultZombiesWarn,
            zombiesCritical: try c.decodeIfPresent(Int.self, forKey: .zombiesCritical) ?? MetricThresholds.defaultZombiesCritical
        )
    }
}

enum SettingsPersistence {
    static let subdirectory = "Towertail"
    static let fileName = "settings.json"

    static func defaultURL() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent(subdirectory, isDirectory: true).appendingPathComponent(fileName)
    }

    static func load(from url: URL = defaultURL()) -> PersistedSettings {
        guard let data = try? Data(contentsOf: url) else {
            return .defaults
        }
        let decoder = JSONDecoder()
        guard let settings = try? decoder.decode(PersistedSettings.self, from: data) else {
            return .defaults
        }
        if settings.nodes.isEmpty {
            var s = settings
            s.nodes = [Node.localMac()]
            return s
        }
        return settings
    }

    @discardableResult
    static func save(_ settings: PersistedSettings, to url: URL = defaultURL()) -> Bool {
        let dir = url.deletingLastPathComponent()
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let data = try encoder.encode(settings)
            try data.write(to: url, options: .atomic)
            return true
        } catch {
            return false
        }
    }
}
