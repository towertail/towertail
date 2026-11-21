import SwiftUI

/// Lets the user pick which hosts go to Cloud. The other hosts stay on SSH.
struct WelcomePickView: View {
    let flow: WelcomeFlow

    private var picked: Int { flow.cloudPicks.count }

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 14) {
                Text("Pick the hosts for Cloud").font(.system(size: 26, weight: .semibold))
                note
                HStack {
                    Text(counter).font(.system(size: 12, weight: .medium))
                    Button("Clear", action: flow.clearPicks)
                        .buttonStyle(.link)
                        .disabled(picked == 0)
                    Spacer()
                }
                list
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

    private var counter: String {
        let hosts = flow.room.map { "\(picked) of \($0) Cloud \($0 == 1 ? "host" : "hosts")" } ?? "\(picked) Cloud \(picked == 1 ? "host" : "hosts")"
        return "\(hosts) selected"
    }

    private var note: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "info.circle").foregroundStyle(Color.accentColor)
            Text(flow.pickNote).font(.system(size: 12)).fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.accentColor.opacity(0.12)))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.accentColor.opacity(0.3)))
    }

    private var list: some View {
        ScrollView {
            VStack(spacing: 0) {
                ForEach(flow.cloudCandidates) { node in
                    row(node)
                    if node.id != flow.cloudCandidates.last?.id { Divider() }
                }
            }
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func row(_ node: Node) -> some View {
        let on = flow.cloudPicks.contains(node.id)
        let full = picked >= (flow.room ?? .max)
        return Toggle(isOn: Binding(get: { on }, set: { _ in flow.togglePick(node.id) })) {
            HStack {
                Text(node.displayName).font(Typography.rowName).lineLimit(1)
                Spacer()
                Text(on ? "Cloud: 24/7 alerts and history" : "SSH: live view on this Mac")
                    .font(.system(size: 12))
                    .foregroundStyle(on ? ThresholdTint.nominal.color : Color.secondary)
            }
        }
        .toggleStyle(.checkbox)
        .disabled(full && !on)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}
