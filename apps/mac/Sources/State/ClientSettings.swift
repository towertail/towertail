import Foundation
import SwiftUI

enum CardDensity: String, CaseIterable, Sendable {
    case a, b
}

/// Local-only client preferences. These never live on a Towertail server —
/// they describe how *this* Mac renders the app (density, terminal choice,
/// launch-at-login). In Remote mode these are still owned here; only
/// `ServerSettings` is pulled from the server.
@Observable
@MainActor
final class ClientSettings {
    var cardDensity: CardDensity
    var launchAtLogin: Bool
    var defaultTerminalApp: String

    private let url: URL

    init(url: URL = SettingsPersistence.defaultURL()) {
        self.url = url
        let p = SettingsPersistence.load(from: url)
        self.cardDensity = CardDensity(rawValue: p.cardDensity) ?? .a
        self.launchAtLogin = p.launchAtLogin
        self.defaultTerminalApp = p.defaultTerminalApp
    }

    static func loadFromDisk() -> ClientSettings {
        ClientSettings()
    }

    func reloadFromDisk() {
        let p = SettingsPersistence.load(from: url)
        self.cardDensity = CardDensity(rawValue: p.cardDensity) ?? .a
        self.launchAtLogin = p.launchAtLogin
        self.defaultTerminalApp = p.defaultTerminalApp
    }

    func persist() {
        let before = SettingsPersistence.load(from: url)
        var p = before
        p.cardDensity = cardDensity.rawValue
        p.launchAtLogin = launchAtLogin
        p.defaultTerminalApp = defaultTerminalApp
        SettingsPersistence.save(p, to: url)
        logSettingsDiff(before: before, after: p, fields: Self.diffFields)
    }

    private static let diffFields: [SettingsField<PersistedSettings>] = [
        SettingsField("cardDensity") { $0.cardDensity },
        SettingsField("launchAtLogin") { "\($0.launchAtLogin)" },
        SettingsField("defaultTerminalApp") { $0.defaultTerminalApp },
    ]
}
