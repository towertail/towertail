import SwiftUI

struct PreferencesWindow: View {
    var body: some View {
        TabView {
            ServersPane()
                .tabItem { Label("Servers", systemImage: "server.rack") }
            ThresholdsPane()
                .tabItem { Label("Thresholds", systemImage: "gauge") }
            NotificationsPane()
                .tabItem { Label("Notifications", systemImage: "bell") }
            GeneralPane()
                .tabItem { Label("General", systemImage: "gear") }
        }
        .frame(minWidth: 600, minHeight: 420)
        .padding()
    }
}
