import SwiftUI

struct WelcomeInstallView: View {
    let flow: WelcomeFlow

    var body: some View {
        if flow.needsPick, !flow.installStarted {
            WelcomePickView(flow: flow)
                .task { await flow.loadRoom() }
        } else {
            installing
        }
    }

    private var installing: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 16) {
                WelcomeHeading(
                    title: "Turn on 24/7 monitoring",
                    subtitle: "Towertail copies the sampler to each server and starts it."
                )
                if let note = flow.planNote {
                    Text(note).font(.system(size: 12)).foregroundStyle(.secondary)
                }
                table
                noSudo
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 16)
            WelcomeFooter(
                step: .install,
                secondary: flow.hasPlanFull && flow.canBuy
                    ? .init(title: "Upgrade for \(flow.upgradeName)", run: flow.upgrade)
                    : nil,
                primary: .init(title: "Done", run: flow.finish)
            )
        }
        .task {
            await flow.loadRoom()
            if !flow.needsPick { await flow.install() }
        }
    }

    private var table: some View {
        CloudInstallTable(
            hosts: flow.added,
            installer: flow.installer,
            failures: flow.addFailures,
            empty: "No servers added. Add servers in Preferences, then turn on 24/7 monitoring."
        )
    }

    private var noSudo: some View {
        HStack(spacing: 10) {
            Image(systemName: "lock.open").foregroundStyle(.secondary)
            Text("The sampler runs as your user in the background. It needs no sudo and no root.")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10).stroke(Color.secondary.opacity(0.3)))
    }
}
