import SwiftUI

/// Picks SSH hosts that exist already and moves them to Cloud.
struct MoveToCloudSheet: View {
    let move: CloudMove

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Move hosts to Cloud").font(.system(size: 20, weight: .semibold))
            switch move.phase {
            case .pick: pick
            case .install: install
            }
        }
        .padding(24)
        .frame(width: 720, height: 560)
    }

    @ViewBuilder
    private var pick: some View {
        CloudPickList(hosts: move.hosts, picks: move.picks, note: move.note, toggle: move.toggle, clear: move.clear)
        Text("The app copies the sampler over SSH and starts it as a service. Then it stops SSH polling for that host. SSH hosts keep the live view, 7 days of history and alerts while this Mac is awake.")
            .font(.system(size: 12))
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
        HStack {
            Spacer()
            Button("Not now", action: move.close)
                .keyboardShortcut(.cancelAction)
            Button(moveTitle) { Task { await move.start() } }
                .keyboardShortcut(.defaultAction)
                .disabled(move.picked.isEmpty)
        }
    }

    @ViewBuilder
    private var install: some View {
        Text("Towertail copies the sampler to each host and starts it. You can close this sheet. The install goes on.")
            .font(.system(size: 12))
            .foregroundStyle(.secondary)
        CloudInstallTable(hosts: move.picked, installer: move.installer)
        if move.notPicked > 0 {
            Text(move.notPicked == 1 ? "The other host stays on this Mac over SSH." : "The other \(move.notPicked) hosts stay on this Mac over SSH.")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
        }
        HStack {
            Spacer()
            Button("Done", action: move.close)
                .keyboardShortcut(.defaultAction)
        }
    }

    private var moveTitle: String {
        let n = move.picked.count
        return n == 0 ? "Move to Cloud" : "Move \(n) \(n == 1 ? "host" : "hosts") to Cloud"
    }
}
