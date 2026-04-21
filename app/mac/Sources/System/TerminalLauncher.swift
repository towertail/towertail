import Foundation
import AppKit

enum TerminalLauncher {
    /// Apps offered in the General Settings picker. Order roughly by
    /// popularity so the common picks surface at the top.
    static let supportedApps: [String] = [
        "Terminal",
        "iTerm",
        "Ghostty",
        "Warp",
        "WezTerm",
        "Alacritty",
        "kitty",
        "Hyper",
    ]

    enum LaunchError: Error, LocalizedError {
        case notSSH
        case missingHost
        case scriptFailed(String)

        var errorDescription: String? {
            switch self {
            case .notSSH: return "Node is not an SSH node"
            case .missingHost: return "SSH host is missing"
            case .scriptFailed(let m): return m
            }
        }
    }

    /// Opens the user's configured terminal app with an `ssh://` URL.
    /// Every supported terminal registers as a handler for `ssh://` (or at
    /// least opens a new window when handed an `ssh://` URL), and this
    /// path doesn't require Automation (Apple Events) entitlement — unlike
    /// driving Terminal.app via AppleScript.
    @discardableResult
    static func openSSH(for node: Node, app: String) -> Result<Void, LaunchError> {
        guard node.kind == .ssh else { return .failure(.notSSH) }
        guard let host = node.sshHost, !host.isEmpty else { return .failure(.missingHost) }
        let user = (node.sshUser?.isEmpty == false) ? node.sshUser! : nil

        var comps = URLComponents()
        comps.scheme = "ssh"
        comps.user = user
        comps.host = host
        guard let sshURL = comps.url else {
            return .failure(.scriptFailed("Couldn't build ssh:// URL"))
        }

        guard let appURL = appURL(for: app) else {
            return .failure(.scriptFailed("Could not find application \(app)"))
        }

        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = true
        NSWorkspace.shared.open([sshURL], withApplicationAt: appURL, configuration: cfg) { _, error in
            if let error = error {
                Logger.shared.warn(
                    "terminal: ssh:// open failed: \(error.localizedDescription)",
                    category: "ui"
                )
            }
        }
        return .success(())
    }

    private static func appURL(for app: String) -> URL? {
        let bid = bundleID(for: app)
        if !bid.isEmpty, let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bid) {
            return url
        }
        return appURL(byName: app)
    }

    private static func bundleID(for app: String) -> String {
        switch app {
        case "Terminal": return "com.apple.Terminal"
        case "iTerm": return "com.googlecode.iterm2"
        case "Ghostty": return "com.mitchellh.ghostty"
        case "Warp": return "dev.warp.Warp-Stable"
        case "WezTerm": return "com.github.wez.wezterm"
        case "Alacritty": return "io.alacritty"
        case "kitty": return "net.kovidgoyal.kitty"
        case "Hyper": return "co.zeit.hyper"
        default: return ""
        }
    }

    private static func appURL(byName name: String) -> URL? {
        let candidates = [
            "/Applications/\(name).app",
            "/Applications/Utilities/\(name).app",
            ("~/Applications/\(name).app" as NSString).expandingTildeInPath,
        ]
        for p in candidates where FileManager.default.fileExists(atPath: p) {
            return URL(fileURLWithPath: p)
        }
        return nil
    }
}
