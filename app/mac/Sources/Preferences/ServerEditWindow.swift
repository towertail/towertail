import SwiftUI

/// Standalone window host for the per-server edit form, opened from a
/// server card's gear button. Presenting the form as a top-level window
/// (rather than a SwiftUI .sheet on the card view) keeps Save working
/// even when the menu-bar popover dismisses: the card unmounts with the
/// popover, but this window survives and its save handler still runs.
struct ServerEditWindow: View {
    let nodeId: UUID?

    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        Group {
            if let nodeId, let node = nodeStore.node(withId: nodeId) {
                ServerEditSheet(context: .edit(node)) { saved in
                    let b = backend
                    Task { try? await b?.updateNode(saved) }
                    dismiss()
                } onCancel: {
                    dismiss()
                }
            } else {
                ContentUnavailableView("Server not found", systemImage: "server.rack")
                    .frame(width: 520, height: 200)
            }
        }
    }
}
