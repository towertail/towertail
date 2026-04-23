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
        logSettingsDiff(before: before, after: p, fields: Self.diffFields)
    }

    private static let diffFields: [SettingsField<PersistedSettings>] = [
        SettingsField("cpuWarn") { "\($0.thresholds.cpuWarn)" },
        SettingsField("cpuCritical") { "\($0.thresholds.cpuCritical)" },
        SettingsField("memWarn") { "\($0.thresholds.memWarn)" },
        SettingsField("memCritical") { "\($0.thresholds.memCritical)" },
        SettingsField("diskWarn") { "\($0.thresholds.diskWarn)" },
        SettingsField("diskCritical") { "\($0.thresholds.diskCritical)" },
        SettingsField("localPollSec") { "\($0.localPollingIntervalSeconds)" },
        SettingsField("sshPollSec") { "\($0.sshPollingIntervalSeconds)" },
        SettingsField("notificationsEnabled") { "\($0.notificationsEnabled)" },
        SettingsField("notifyWarn") { "\($0.notifyWarn)" },
        SettingsField("notifyCritical") { "\($0.notifyCritical)" },
        SettingsField("notifyDebounceSec") { "\($0.notifyDebounceSeconds)" },
        SettingsField("autoUpdateSamplers") { "\($0.autoUpdateSamplersEnabled)" },
        SettingsField("postWakeGraceSec") { "\($0.postWakeGraceSeconds)" },
    ]
}
