import SwiftUI

/// The line under the list, in the Cards and in the Rows view. It shows the
/// plan and the host count with a link to the dashboard when the user is
/// signed in. The Rows view adds the rx/tx legend.
struct PopoverFooter: View {
    let account: CloudAccount?
    let showsLegend: Bool
    @Environment(\.openURL) private var openURL

    var body: some View {
        let summary = account.flatMap { Self.summary(for: $0) }
        if summary != nil || showsLegend {
            HStack(spacing: 4) {
                if let summary {
                    Text(summary).foregroundStyle(.secondary)
                    if showsLegend { Text("·").foregroundStyle(.secondary) }
                }
                if showsLegend {
                    Text("↓ rx").foregroundStyle(RowsLayout.netRx)
                    Text("↑ tx").foregroundStyle(RowsLayout.netTx)
                }
                Spacer(minLength: 0)
                if let account, summary != nil {
                    Button("Dashboard") { Task { await account.openDashboard(using: { openURL($0) }) } }
                        .buttonStyle(.link)
                        .pointingHandOnHover()
                }
            }
            .font(Typography.metaText)
            .padding(.horizontal, RowsLayout.inset)
            .padding(.vertical, 10)
            .overlay(alignment: .top) { Divider().opacity(0.3) }
        }
    }

    /// "Solo · 8 of 10 hosts". Nil when the user is signed out.
    static func summary(for account: CloudAccount) -> String? {
        guard case .signedIn(let me) = account.status else { return nil }
        guard let billing = account.billing else { return me.org.plan.capitalized }
        return "\(billing.plan.id.capitalized) · \(billing.nodes) of \(billing.plan.maxNodes) hosts"
    }
}
