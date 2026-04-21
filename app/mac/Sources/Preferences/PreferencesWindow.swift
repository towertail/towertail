import SwiftUI

struct PreferencesWindow: View {
    enum Tab: String, CaseIterable, Hashable {
        case servers, thresholds, notifications, general

        var title: String {
            switch self {
            case .servers: return "Servers"
            case .thresholds: return "Thresholds"
            case .notifications: return "Notifications"
            case .general: return "General"
            }
        }

        var icon: String {
            switch self {
            case .servers: return "server.rack"
            case .thresholds: return "gauge"
            case .notifications: return "bell"
            case .general: return "gear"
            }
        }
    }

    @State private var selection: Tab = .servers

    var body: some View {
        // Each pane's content is gated on `selection` so the Servers
        // `Table` is actually torn down when the user navigates away.
        // Leaving the Table alive under TabView crashes on tab switch
        // (`AGGraphGetAttributeSubgraph` precondition, macOS 15 /
        // SwiftUI 6). The empty placeholders preserve TabView's native
        // top-centered chrome while the real content only exists for
        // the active tab.
        TabView(selection: $selection) {
            paneContent(for: .servers)
                .tabItem { Label(Tab.servers.title, systemImage: Tab.servers.icon) }
                .tag(Tab.servers)

            paneContent(for: .thresholds)
                .tabItem { Label(Tab.thresholds.title, systemImage: Tab.thresholds.icon) }
                .tag(Tab.thresholds)

            paneContent(for: .notifications)
                .tabItem { Label(Tab.notifications.title, systemImage: Tab.notifications.icon) }
                .tag(Tab.notifications)

            paneContent(for: .general)
                .tabItem { Label(Tab.general.title, systemImage: Tab.general.icon) }
                .tag(Tab.general)
        }
        .frame(minWidth: 600, minHeight: 420)
        .padding()
        .navigationTitle(selection.title)
    }

    /// Returns the real pane only for the active tab; other tabs get a
    /// transparent placeholder. This keeps `TabView`'s native chrome
    /// (centered tabs with icons on top) while making sure the heavy
    /// `Table` inside `ServersPane` is fully destroyed when any other
    /// tab is active — avoids the `AGGraphGetAttributeSubgraph`
    /// precondition crash on tab switch (macOS 15 / SwiftUI 6).
    @ViewBuilder
    private func paneContent(for tab: Tab) -> some View {
        if tab == selection {
            switch tab {
            case .servers: ServersPane()
            case .thresholds: ThresholdsPane()
            case .notifications: NotificationsPane()
            case .general: GeneralPane()
            }
        } else {
            Color.clear
        }
    }
}
