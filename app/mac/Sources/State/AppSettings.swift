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
        var p = SettingsPersistence.load(from: url)
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
    }
}
