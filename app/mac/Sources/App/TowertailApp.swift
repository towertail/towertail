import SwiftUI

@main
struct TowertailApp: App {
    @State private var env = AppEnvironment()

    var body: some Scene {
        MenuBarExtra {
            PopoverRoot()
                .environment(env.store)
                .environment(env.settings)
                .task { env.start() }
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
        }
    }
}
