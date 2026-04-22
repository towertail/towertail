import SwiftUI
import AppKit

@main
struct TowertailApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var env = AppEnvironment()

    init() {
        // Start the collector pacer as soon as the app launches, not when
        // the menu-bar popover is first opened. Otherwise the sampler sits
        // idle and no history accumulates until the user clicks the icon.
        env.start()
    }

    var body: some Scene {
        MenuBarExtra {
            PopoverRoot()
                .environment(env.store)
                .environment(env.settings)
                .environment(env.nodeStore)
                .environment(env.samplerUpdater)
        } label: {
            // The menu-bar label is instantiated eagerly at launch (unlike
            // the popover content). Piggyback the tap-routing installer
            // here so the delegate has a way to open a full-view window as
            // soon as the app is running, not just after the user has
            // clicked the menu-bar icon.
            ZStack {
                MenuBarIcon(store: env.store)
                SceneTapInstaller()
            }
        }
        .menuBarExtraStyle(.window)
        .commands {
            CommandGroup(replacing: .appTermination) {
                Button("Quit Towertail") {
                    NSApp.terminate(nil)
                }
                .keyboardShortcut("q", modifiers: .command)
            }
        }

        Settings {
            PreferencesWindow()
                .environment(env.store)
                .environment(env.settings)
                .environment(env.nodeStore)
                .environment(env.samplerUpdater)
                // Injected so the Servers pane's Version column can show
                // "updating…" while a push is in flight. Safe now that
                // PreferencesWindow only instantiates the active tab's
                // content (see paneContent) — the Table-in-TabView
                // observation crash no longer applies.
                // Settings is an .accessory-policy scene by default, so
                // without flipping activation policy the app never gets a
                // Dock icon while it's open — meaning the user can't ⌘-Tab
                // or Mission Control their way back to it. Acquire on
                // appear, release on disappear (same pattern as the
                // full-view chart window).
                .onAppear { ActivationPolicyCoordinator.shared.acquire() }
                .onDisappear { ActivationPolicyCoordinator.shared.release() }
        }

        WindowGroup(id: "full-view", for: FullViewContext.self) { $ctx in
            Group {
                if let ctx {
                    FullViewWindow(context: ctx)
                        .environment(env.store)
                        .environment(env.settings)
                        .environment(env.nodeStore)
                } else {
                    ContentUnavailableView("No host", systemImage: "server.rack")
                }
            }
        }
        .windowResizability(.contentSize)
        .defaultSize(width: 1080, height: 760)

        // Per-server edit, opened from a card's gear button. A real window
        // (rather than a SwiftUI .sheet on the card) survives the popover
        // auto-closing when the sheet takes focus — otherwise Save never
        // fires because the card unmounts along with the popover.
        WindowGroup(id: "server-edit", for: UUID.self) { $nodeId in
            ServerEditWindow(nodeId: nodeId)
                .environment(env.nodeStore)
                .environment(env.settings)
                .environment(env.store)
                .onAppear { ActivationPolicyCoordinator.shared.acquire() }
                .onDisappear { ActivationPolicyCoordinator.shared.release() }
        }
        .windowResizability(.contentSize)
    }
}

/// App delegate: the one place we can reliably wire cross-app hooks at
/// launch regardless of which SwiftUI scenes happen to be on screen. The
/// status-bar popover and the full-view WindowGroup both build lazily, so
/// wiring notification-tap routing here (rather than inside a View body) is
/// the only way to handle a tap when neither is currently instantiated.
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NotificationTapRouter.shared.setHandler { ctx in
            Task { @MainActor in
                AppDelegate.opener?(ctx)
            }
        }
    }

    /// Set by `SceneTapInstaller` once the scene graph has `openWindow`
    /// available. AppKit-only code paths (e.g. the notification delegate
    /// callback) use this to route to SwiftUI's WindowGroup.
    @MainActor static var opener: ((FullViewContext) -> Void)?
}

/// Zero-size view that captures `openWindow` from the scene environment
/// and publishes it to the AppDelegate. Attached to the menu-bar label
/// (which instantiates eagerly) so a tap immediately after launch still
/// finds a handler.
private struct SceneTapInstaller: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Color.clear
            .frame(width: 0, height: 0)
            .onAppear {
                AppDelegate.opener = { ctx in
                    openWindow(id: "full-view", value: ctx)
                    NSApp.activate(ignoringOtherApps: true)
                }
            }
    }
}
