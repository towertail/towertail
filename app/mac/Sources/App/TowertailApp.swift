import SwiftUI

@main
struct TowertailApp: App {
    @State private var env = AppEnvironment()

    init() {
        // Start the collector pacer as soon as the app launches, not when
        // the menu-bar popover is first opened. Otherwise the agent sits
        // idle and no history accumulates until the user clicks the icon.
        env.start()
    }

    var body: some Scene {
        MenuBarExtra {
            PopoverRoot()
                .environment(env.store)
                .environment(env.settings)
                .environment(env.nodeStore)
        } label: {
            MenuBarIcon(state: env.store.aggregateState)
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
        }

        WindowGroup(id: "full-view", for: FullViewContext.self) { $ctx in
            Group {
                if let ctx {
                    FullViewWindow(context: ctx)
                        .environment(env.store)
                        .environment(env.settings)
                } else {
                    ContentUnavailableView("No host", systemImage: "server.rack")
                }
            }
        }
        .windowResizability(.contentSize)
        .defaultSize(width: 1080, height: 760)
    }
}
