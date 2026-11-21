import Foundation
import SwiftUI

enum CardDensity: String, CaseIterable, Sendable {
    case a, b
}

/// Body of the menu-bar popover: one card for each server, or one compact row.
enum PopoverLayout: String, CaseIterable, Sendable {
    case rows, cards

    static let standard: PopoverLayout = .rows
}

/// `pulse` colors only the LEDs and the pulse line. `full` also colors the rack at warn and critical.
enum MenuBarIconStyle: String, CaseIterable, Sendable {
    case pulse, full
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
    var menuBarIconStyle: MenuBarIconStyle
    var welcomeDone: Bool
    var popoverLayout: PopoverLayout

    private let url: URL

    init(url: URL = SettingsPersistence.defaultURL()) {
        self.url = url
        let p = SettingsPersistence.load(from: url)
        self.cardDensity = CardDensity(rawValue: p.cardDensity) ?? .a
        self.launchAtLogin = p.launchAtLogin
        self.defaultTerminalApp = p.defaultTerminalApp
        self.menuBarIconStyle = MenuBarIconStyle(rawValue: p.menuBarIconStyle) ?? .pulse
        self.welcomeDone = p.welcomeDone
        self.popoverLayout = PopoverLayout(rawValue: p.popoverLayout) ?? .standard
    }

    static func loadFromDisk() -> ClientSettings {
        ClientSettings()
    }

    func reloadFromDisk() {
        let p = SettingsPersistence.load(from: url)
        self.cardDensity = CardDensity(rawValue: p.cardDensity) ?? .a
        self.launchAtLogin = p.launchAtLogin
        self.defaultTerminalApp = p.defaultTerminalApp
        self.menuBarIconStyle = MenuBarIconStyle(rawValue: p.menuBarIconStyle) ?? .pulse
        self.welcomeDone = p.welcomeDone
        self.popoverLayout = PopoverLayout(rawValue: p.popoverLayout) ?? .standard
    }

    func persist() {
        let before = SettingsPersistence.load(from: url)
        var p = before
        p.cardDensity = cardDensity.rawValue
        p.launchAtLogin = launchAtLogin
        p.defaultTerminalApp = defaultTerminalApp
        p.menuBarIconStyle = menuBarIconStyle.rawValue
        p.welcomeDone = welcomeDone
        p.popoverLayout = popoverLayout.rawValue
        SettingsPersistence.save(p, to: url)
        logSettingsDiff(before: before, after: p, fields: Self.diffFields)
    }

    private static let diffFields: [SettingsField<PersistedSettings>] = [
        SettingsField("cardDensity") { $0.cardDensity },
        SettingsField("launchAtLogin") { "\($0.launchAtLogin)" },
        SettingsField("defaultTerminalApp") { $0.defaultTerminalApp },
        SettingsField("menuBarIconStyle") { $0.menuBarIconStyle },
        SettingsField("welcomeDone") { "\($0.welcomeDone)" },
        SettingsField("popoverLayout") { $0.popoverLayout },
    ]
}
