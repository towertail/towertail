import Foundation

struct PersistedSettings: Codable, Equatable {
    var nodes: [Node]
    var thresholds: PersistedThresholds
    var pollingIntervalSeconds: Int
    var cardDensity: String
    var notificationsEnabled: Bool
    var notifyWarn: Bool
    var notifyCritical: Bool
    var notifyDebounceSeconds: Int
    var launchAtLogin: Bool

    static let defaults = PersistedSettings(
        nodes: [Node.localMac()],
        thresholds: .defaults,
        pollingIntervalSeconds: 15,
        cardDensity: "a",
        notificationsEnabled: false,
        notifyWarn: true,
        notifyCritical: true,
        notifyDebounceSeconds: 60,
        launchAtLogin: false
    )
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
