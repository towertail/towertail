import AppKit

/// Full Disk Access of the app. macOS has no API to ask for it, so read
/// a folder that only apps with the grant can list.
/// Ad-hoc signed dev builds lose the grant on each build.
enum FullDiskAccess {
    static var isGranted: Bool {
        let home = FileManager.default.homeDirectoryForCurrentUser
        for sub in ["Library/Safari", "Library/Mail", "Library/Messages"] {
            let path = home.appendingPathComponent(sub).path
            guard FileManager.default.fileExists(atPath: path) else { continue }
            return (try? FileManager.default.contentsOfDirectory(atPath: path)) != nil
        }
        return true
    }

    /// Opens System Settings › Privacy & Security › Full Disk Access.
    static func openSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles") {
            NSWorkspace.shared.open(url)
        }
    }
}
