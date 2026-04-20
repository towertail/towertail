import SwiftUI

struct PreferencesWindow: View {
    var body: some View {
        TabView {
            ComingSoonPane(title: "Servers")
                .tabItem { Label("Servers", systemImage: "server.rack") }
            ComingSoonPane(title: "Thresholds")
                .tabItem { Label("Thresholds", systemImage: "gauge") }
            ComingSoonPane(title: "Notifications")
                .tabItem { Label("Notifications", systemImage: "bell") }
            ComingSoonPane(title: "General")
                .tabItem { Label("General", systemImage: "gear") }
        }
        .frame(minWidth: 540, minHeight: 360)
    }
}

struct ComingSoonPane: View {
    let title: String
    var body: some View {
        VStack(spacing: 12) {
            Text(title)
                .font(.title2).bold()
            Text("Coming soon")
                .font(.body)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}
