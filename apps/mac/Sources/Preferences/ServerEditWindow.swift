import SwiftUI

/// Standalone window for one server's settings, opened from a server card's
/// gear button. A real window survives the menu-bar popover closing, so
/// edits still apply after the card unmounts.
struct ServerEditWindow: View {
    let nodeId: UUID?

    @Environment(NodeStore.self) private var nodeStore
    @State private var tester = ServerTester()

    var body: some View {
        Group {
            if let nodeId, let node = nodeStore.node(withId: nodeId) {
                VStack(spacing: 0) {
                    ServerDetailView(node: node, tester: tester)
                    if let o = tester.outcome {
                        Text(o.message)
                            .font(.callout)
                            .foregroundStyle(o.ok ? Color.secondary : ThresholdTint.critical.color)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 20)
                            .padding(.vertical, 8)
                            .background(.bar)
                    }
                }
                .frame(width: 560, height: 620)
                .navigationTitle(node.displayName)
            } else {
                ContentUnavailableView("Server not found", systemImage: "server.rack")
                    .frame(width: 560, height: 200)
            }
        }
    }
}
