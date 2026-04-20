import SwiftUI

struct PopoverRoot: View {
    @Environment(ServerStore.self) private var store

    var body: some View {
        VStack(spacing: 0) {
            PopoverHeader()
            Divider().opacity(0.3)
            ScrollView {
                LazyVStack(spacing: 10) {
                    ForEach(store.serverVMs) { vm in
                        ServerCardView(vm: vm)
                    }
                }
                .padding(10)
            }
        }
        .frame(width: 360, height: 620)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}
