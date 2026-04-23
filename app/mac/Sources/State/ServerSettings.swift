import Foundation
import SwiftUI

/// Settings that the Towertail server owns in Remote mode — thresholds,
/// polling intervals, notification policy, sampler auto-update, post-wake
/// grace. In Local mode they live here on disk (same JSON file as
/// `ClientSettings`) and are mutated by the Preferences panes as before.
@Observable
@MainActor
final class ServerSettings {
    var thresholds: MetricThresholds
    var localPollingIntervalSeconds: Int
    var sshPollingIntervalSeconds: Int
    var notificationsEnabled: Bool
    var notifyWarn: Bool
    var notifyCritical: Bool
    var notifyDebounceSeconds: Int
    var autoUpdateSamplersEnabled: Bool
    /// Seconds after Mac wake / network-return during which collectors
    /// keep polling but notifications are suppressed. Short window that
    /// lets DHCP + Tailscale resettle without paging on the first failed
    /// post-wake dial. 0 disables the grace period entirely.
    var postWakeGraceSeconds: Int

    private let url: URL

    init(url: URL = SettingsPersistence.defaultURL()) {
        self.url = url
        let p = SettingsPersistence.load(from: url)
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
        self.autoUpdateSamplersEnabled = p.autoUpdateSamplersEnabled
        self.postWakeGraceSeconds = max(0, min(300, p.postWakeGraceSeconds))
    }

    static func loadFromDisk() -> ServerSettings {
        ServerSettings()
    }

    func reloadFromDisk() {
        let p = SettingsPersistence.load(from: url)
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
        self.autoUpdateSamplersEnabled = p.autoUpdateSamplersEnabled
        self.postWakeGraceSeconds = max(0, min(300, p.postWakeGraceSeconds))
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
        p.autoUpdateSamplersEnabled = autoUpdateSamplersEnabled
        p.postWakeGraceSeconds = postWakeGraceSeconds
        SettingsPersistence.save(p, to: url)
        logDiff(before: before, after: p)
    }

    private func logDiff(before: PersistedSettings, after: PersistedSettings) {
        var changes: [String: String] = [:]
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
        if before.autoUpdateSamplersEnabled != after.autoUpdateSamplersEnabled {
            changes["autoUpdateSamplers"] = "\(before.autoUpdateSamplersEnabled)→\(after.autoUpdateSamplersEnabled)"
        }
        if before.postWakeGraceSeconds != after.postWakeGraceSeconds {
            changes["postWakeGraceSec"] = "\(before.postWakeGraceSeconds)→\(after.postWakeGraceSeconds)"
        }
        if changes.isEmpty { return }
        Logger.shared.info(
            "settings: changed",
            category: "settings",
            kv: changes
        )
    }
}
