import AppKit

/// Reference-counts "windows that need a Dock icon + ⌘-Tab entry" and toggles
/// NSApp's activation policy accordingly. LSUIElement: true starts us as
/// .accessory (menu bar only); we flip to .regular while any full-view window
/// is open, then back to .accessory when the last one closes.
@MainActor
final class ActivationPolicyCoordinator {
    static let shared = ActivationPolicyCoordinator()

    private var count: Int = 0

    private init() {}

    func acquire() {
        count += 1
        if count == 1 {
            NSApp.setActivationPolicy(.regular)
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    /// Activates the app and raises the window that SwiftUI just opened or
    /// re-focused. Call after `openWindow` / `SettingsLink`. The delay lets
    /// SwiftUI order the window and the popover close first; without it an
    /// already-open window stays behind other apps.
    func bringToFront() {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) {
            NSApp.activate(ignoringOtherApps: true)
            let target = NSApp.orderedWindows.first {
                $0.isVisible && !($0 is NSPanel) && $0.canBecomeMain
            }
            target?.makeKeyAndOrderFront(nil)
        }
    }

    func release() {
        count = max(0, count - 1)
        if count == 0 {
            NSApp.setActivationPolicy(.accessory)
        }
    }
}
