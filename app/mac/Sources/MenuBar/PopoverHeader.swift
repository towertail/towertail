import SwiftUI
import AppKit

struct PopoverHeader: View {
    @Environment(ServerStore.self) private var store

    var body: some View {
        let s = store.summary
        HStack(spacing: 8) {
            Image(systemName: "server.rack")
                .foregroundStyle(.primary)
            Text("Towertail")
                .font(Typography.headerTitle)
            Spacer(minLength: 4)
            Text("\(s.online) online · \(s.warn) warn · \(s.down) down")
                .font(Typography.metaText)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            Spacer(minLength: 4)
            SettingsLink {
                Image(systemName: "gearshape")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .help("Settings")
            .simultaneousGesture(TapGesture().onEnded {
                NSApp.activate(ignoringOtherApps: true)
            })
            Button {
                NSApp.terminate(nil)
            } label: {
                Image(systemName: "power")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .help("Quit Towertail")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(height: 36)
        .background(Color.black.opacity(0.001))
    }
}
