import SwiftUI

/// What is wrong now. Local and SSH hosts keep no alert history.
struct AlertsTab: View {
    let vm: ServerViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if vm.liveSource == .cloud {
                note("Alert history for cloud hosts is on the web.")
            } else {
                current
                note("This Mac keeps no alert history for local hosts.")
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14).padding(.bottom, 12)
    }

    private var current: some View {
        VStack(alignment: .leading, spacing: 0) {
            KLabel("Now").padding(.horizontal, 14).padding(.vertical, 10)
            let reasons = vm.health.reasons
            if reasons.isEmpty {
                Text("No open alert on this host.")
                    .font(.callout).foregroundStyle(.secondary)
                    .padding(.horizontal, 14).padding(.bottom, 12)
            }
            ForEach(Array(reasons.enumerated()), id: \.offset) { _, r in
                Divider().overlay(WB.border)
                HStack(spacing: 10) {
                    Circle().fill(r.tint.color).frame(width: 8, height: 8)
                    Text(r.text).font(.system(size: 13))
                    Spacer()
                }
                .padding(.horizontal, 14).padding(.vertical, 8)
            }
        }
        .wbCard()
    }

    private func note(_ text: String) -> some View {
        Text(text).font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
    }
}

/// The alert rules and the connection of one host.
struct SettingsTab: View {
    let vm: ServerViewModel
    let node: Node?
    @State private var tester = ServerTester()

    var body: some View {
        Group {
            if vm.liveSource == .cloud {
                Text("Rules of cloud hosts are set on the web.")
                    .font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(14)
            } else if let node {
                ServerDetailView(node: node, tester: tester, tab: .alerts)
                    .id(node.id)
            } else {
                ContentUnavailableView("No settings", systemImage: "gearshape")
            }
        }
    }
}
