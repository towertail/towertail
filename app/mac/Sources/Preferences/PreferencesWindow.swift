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
        TabView(selection: $selection) {
            ServersPane()
                .tabItem { Label(Tab.servers.title, systemImage: Tab.servers.icon) }
                .tag(Tab.servers)
            ThresholdsPane()
                .tabItem { Label(Tab.thresholds.title, systemImage: Tab.thresholds.icon) }
                .tag(Tab.thresholds)
            NotificationsPane()
                .tabItem { Label(Tab.notifications.title, systemImage: Tab.notifications.icon) }
                .tag(Tab.notifications)
            GeneralPane()
                .tabItem { Label(Tab.general.title, systemImage: Tab.general.icon) }
                .tag(Tab.general)
        }
        .frame(minWidth: 600, minHeight: 420)
        .padding()
        .navigationTitle(selection.title)
        // The Settings scene's default title bar doesn't display an icon
        // next to the title; inject one via a toolbar principal item so
        // switching tabs updates both the label and its glyph.
        .toolbar {
            ToolbarItem(placement: .principal) {
                HStack(spacing: 6) {
                    Image(systemName: selection.icon)
                        .foregroundStyle(.tint)
                    Text(selection.title)
                        .font(.headline)
                }
            }
        }
    }
}
