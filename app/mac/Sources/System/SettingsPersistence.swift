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
        defaultTerminalApp: "Terminal"
    )

    enum CodingKeys: String, CodingKey {
        case nodes, thresholds
        case localPollingIntervalSeconds, sshPollingIntervalSeconds
        case cardDensity
        case notificationsEnabled, notifyWarn, notifyCritical, notifyDebounceSeconds
        case launchAtLogin
        case autoUpdateSamplersEnabled
        case defaultTerminalApp
        case pollingIntervalSeconds // legacy single-value field
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
        self.launchAtLogin = try c.decode(Bool.self, forKey: .launchAtLogin)
        self.autoUpdateSamplersEnabled = try c.decodeIfPresent(Bool.self, forKey: .autoUpdateSamplersEnabled)
            ?? PersistedSettings.defaults.autoUpdateSamplersEnabled
        self.defaultTerminalApp = try c.decodeIfPresent(String.self, forKey: .defaultTerminalApp)
            ?? PersistedSettings.defaults.defaultTerminalApp

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
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(nodes, forKey: .nodes)
        try c.encode(thresholds, forKey: .thresholds)
        try c.encode(localPollingIntervalSeconds, forKey: .localPollingIntervalSeconds)
        try c.encode(sshPollingIntervalSeconds, forKey: .sshPollingIntervalSeconds)
        try c.encode(cardDensity, forKey: .cardDensity)
        try c.encode(notificationsEnabled, forKey: .notificationsEnabled)
        try c.encode(notifyWarn, forKey: .notifyWarn)
        try c.encode(notifyCritical, forKey: .notifyCritical)
        try c.encode(notifyDebounceSeconds, forKey: .notifyDebounceSeconds)
        try c.encode(launchAtLogin, forKey: .launchAtLogin)
        try c.encode(autoUpdateSamplersEnabled, forKey: .autoUpdateSamplersEnabled)
        try c.encode(defaultTerminalApp, forKey: .defaultTerminalApp)
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
        defaultTerminalApp: String
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
    }
}

struct PersistedThresholds: Codable, Equatable {
    var cpuWarn: Double
    var cpuCritical: Double
    var memWarn: Double
    var memCritical: Double
    var diskWarn: Double
    var diskCritical: Double

    static let defaults = PersistedThresholds(
        cpuWarn: 0.75, cpuCritical: 0.90,
        memWarn: 0.75, memCritical: 0.90,
        diskWarn: 0.85, diskCritical: 0.95
    )
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
