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

    func release() {
        count = max(0, count - 1)
        if count == 0 {
            NSApp.setActivationPolicy(.accessory)
        }
    }
}
