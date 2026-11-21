import SwiftUI

/// Lets the user pick which hosts go to Cloud. The other hosts stay on SSH.
struct WelcomePickView: View {
    let flow: WelcomeFlow

    private var picked: Int { flow.cloudPicks.count }

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 14) {
                Text("Pick the hosts for Cloud").font(.system(size: 26, weight: .semibold))
                CloudPickList(
                    hosts: flow.cloudCandidates,
                    picks: flow.picks,
                    note: flow.pickNote,
                    toggle: flow.togglePick,
                    clear: flow.clearPicks
                )
                Text("SSH hosts keep the live view, 7 days of history and alerts while this Mac is awake. Move a host later in Settings › Servers.")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 16)
            WelcomeFooter(
                step: .install,
                secondary: .init(title: "Keep all on SSH", run: flow.keepAllOnSSH),
                primary: picked == 0
                    ? .init(title: "Continue", run: flow.keepAllOnSSH)
                    : .init(
                        title: "Move \(picked) \(picked == 1 ? "host" : "hosts") to Cloud",
                        run: { Task { await flow.install() } }
                    )
            )
        }
    }
}
