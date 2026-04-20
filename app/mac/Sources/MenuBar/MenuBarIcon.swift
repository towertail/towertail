import SwiftUI

struct MenuBarIcon: View {
    let state: AggregateState

    var body: some View {
        Image(systemName: state == .critical ? "server.rack.badge.exclamationmark" : "server.rack")
            .symbolRenderingMode(.palette)
            .foregroundStyle(.primary, state.tint)
    }
}
