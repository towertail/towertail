import Foundation
import SwiftUI

enum CardDensity: String, CaseIterable, Sendable {
    case a, b
}

@Observable
@MainActor
final class AppSettings {
    var cardDensity: CardDensity
    var thresholds: MetricThresholds
    var localPollingIntervalSeconds: Int
    var sshPollingIntervalSeconds: Int
    var notificationsEnabled: Bool
    var notifyWarn: Bool
    var notifyCritical: Bool
    var notifyDebounceSeconds: Int
    var launchAtLogin: Bool
    var autoUpdateSamplersEnabled: Bool

    private let url: URL

    init(url: URL = SettingsPersistence.defaultURL()) {
        self.url = url
        let p = SettingsPersistence.load(from: url)
        self.cardDensity = CardDensity(rawValue: p.cardDensity) ?? .a
        self.thresholds = MetricThresholds(
            cpuWarn: p.thresholds.cpuWarn, cpuCritical: p.thresholds.cpuCritical,
            memWarn: p.thresholds.memWarn, memCritical: p.thresholds.memCritical,
            diskWarn: p.thresholds.diskWarn, diskCritical: p.thresholds.diskCritical
        )
        self.localPollingIntervalSeconds = max(1, min(300, p.localPollingIntervalSeconds))
        self.sshPollingIntervalSeconds = max(1, min(300, p.sshPollingIntervalSeconds))
        self.notificationsEnabled = p.notificationsEnabled
        self.notifyWarn = p.notifyWarn
        self.notifyCritical = p.notifyCritical
        self.notifyDebounceSeconds = p.notifyDebounceSeconds
        self.launchAtLogin = p.launchAtLogin
        self.autoUpdateSamplersEnabled = p.autoUpdateSamplersEnabled
    }

    static func loadFromDisk() -> AppSettings {
        AppSettings()
    }

    func pollingInterval(for kind: NodeKind) -> Int {
        switch kind {
        case .local: return localPollingIntervalSeconds
        case .ssh: return sshPollingIntervalSeconds
        }
    }

    func persist() {
        let before = SettingsPersistence.load(from: url)
        var p = before
        p.cardDensity = cardDensity.rawValue
        p.thresholds = PersistedThresholds(
            cpuWarn: thresholds.cpuWarn, cpuCritical: thresholds.cpuCritical,
            memWarn: thresholds.memWarn, memCritical: thresholds.memCritical,
            diskWarn: thresholds.diskWarn, diskCritical: thresholds.diskCritical
        )
        p.localPollingIntervalSeconds = localPollingIntervalSeconds
        p.sshPollingIntervalSeconds = sshPollingIntervalSeconds
        p.notificationsEnabled = notificationsEnabled
        p.notifyWarn = notifyWarn
        p.notifyCritical = notifyCritical
        p.notifyDebounceSeconds = notifyDebounceSeconds
        p.launchAtLogin = launchAtLogin
        p.autoUpdateSamplersEnabled = autoUpdateSamplersEnabled
        SettingsPersistence.save(p, to: url)
        logDiff(before: before, after: p)
    }

    /// Emit one "settings: changed" log line with only the fields that
    /// differ. Keeps noisy persist-on-slider-drag events from flooding
    /// the log file while still leaving a clear audit trail of what the
    /// user changed and when.
    private func logDiff(before: PersistedSettings, after: PersistedSettings) {
        var changes: [String: String] = [:]
        if before.cardDensity != after.cardDensity {
            changes["cardDensity"] = "\(before.cardDensity)→\(after.cardDensity)"
        }
        if before.thresholds.cpuWarn != after.thresholds.cpuWarn {
            changes["cpuWarn"] = "\(before.thresholds.cpuWarn)→\(after.thresholds.cpuWarn)"
        }
        if before.thresholds.cpuCritical != after.thresholds.cpuCritical {
            changes["cpuCritical"] = "\(before.thresholds.cpuCritical)→\(after.thresholds.cpuCritical)"
        }
        if before.thresholds.memWarn != after.thresholds.memWarn {
            changes["memWarn"] = "\(before.thresholds.memWarn)→\(after.thresholds.memWarn)"
        }
        if before.thresholds.memCritical != after.thresholds.memCritical {
            changes["memCritical"] = "\(before.thresholds.memCritical)→\(after.thresholds.memCritical)"
        }
        if before.thresholds.diskWarn != after.thresholds.diskWarn {
            changes["diskWarn"] = "\(before.thresholds.diskWarn)→\(after.thresholds.diskWarn)"
        }
        if before.thresholds.diskCritical != after.thresholds.diskCritical {
            changes["diskCritical"] = "\(before.thresholds.diskCritical)→\(after.thresholds.diskCritical)"
        }
        if before.localPollingIntervalSeconds != after.localPollingIntervalSeconds {
            changes["localPollSec"] = "\(before.localPollingIntervalSeconds)→\(after.localPollingIntervalSeconds)"
        }
        if before.sshPollingIntervalSeconds != after.sshPollingIntervalSeconds {
            changes["sshPollSec"] = "\(before.sshPollingIntervalSeconds)→\(after.sshPollingIntervalSeconds)"
        }
        if before.notificationsEnabled != after.notificationsEnabled {
            changes["notificationsEnabled"] = "\(before.notificationsEnabled)→\(after.notificationsEnabled)"
        }
        if before.notifyWarn != after.notifyWarn {
            changes["notifyWarn"] = "\(before.notifyWarn)→\(after.notifyWarn)"
        }
        if before.notifyCritical != after.notifyCritical {
            changes["notifyCritical"] = "\(before.notifyCritical)→\(after.notifyCritical)"
        }
        if before.notifyDebounceSeconds != after.notifyDebounceSeconds {
            changes["notifyDebounceSec"] = "\(before.notifyDebounceSeconds)→\(after.notifyDebounceSeconds)"
        }
        if before.launchAtLogin != after.launchAtLogin {
            changes["launchAtLogin"] = "\(before.launchAtLogin)→\(after.launchAtLogin)"
        }
        if before.autoUpdateSamplersEnabled != after.autoUpdateSamplersEnabled {
            changes["autoUpdateSamplers"] = "\(before.autoUpdateSamplersEnabled)→\(after.autoUpdateSamplersEnabled)"
        }
        if changes.isEmpty { return }
        Logger.shared.info(
            "settings: changed",
            category: "settings",
            kv: changes
        )
    }
}
