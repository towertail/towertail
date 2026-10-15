import Foundation
import ServiceManagement

@MainActor
final class LaunchAtLogin {
    static let shared = LaunchAtLogin()

    private(set) var lastError: String?

    private init() {}

    var isEnabled: Bool {
        if #available(macOS 13.0, *) {
            return SMAppService.mainApp.status == .enabled
        }
        return false
    }

    @discardableResult
    func setEnabled(_ enabled: Bool) -> Bool {
        if #available(macOS 13.0, *) {
            do {
                if enabled {
                    try SMAppService.mainApp.register()
                } else {
                    try SMAppService.mainApp.unregister()
                }
                lastError = nil
                return true
            } catch {
                lastError = error.localizedDescription
                return false
            }
        }
        lastError = "Requires macOS 13+"
        return false
    }
}
