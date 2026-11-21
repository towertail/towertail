import SwiftUI

/// Lets the user pick which hosts go to Cloud when the plan has less room
/// than the number of hosts.
struct WelcomePickView: View {
    let flow: WelcomeFlow

    private var room: Int { flow.room ?? 0 }
    private var picked: Int { flow.cloudPicks.count }

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 16) {
                WelcomeHeading(title: "Pick the hosts for Cloud", subtitle: flow.pickNote)
                HStack {
                    Text("\(picked) of \(room) selected")
                        .font(.system(size: 12, weight: .medium))
                    Button("Clear", action: flow.clearPicks)
                        .buttonStyle(.link)
                        .disabled(picked == 0)
                    Spacer()
                }
                list
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 16)
            WelcomeFooter(
                step: .install,
                primary: .init(
                    title: "Move \(picked) \(picked == 1 ? "host" : "hosts") to Cloud",
                    enabled: picked > 0,
                    run: { Task { await flow.install() } }
                )
            )
        }
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
        let full = picked >= room
        return Toggle(isOn: Binding(get: { on }, set: { _ in flow.togglePick(node.id) })) {
            HStack {
                Text(node.displayName).font(Typography.rowName).lineLimit(1)
                Spacer()
                Text(on ? "Goes to Cloud" : "Stays on this Mac over SSH")
                    .font(.system(size: 11))
                    .foregroundStyle(on ? Color.primary : Color.secondary)
            }
        }
        .toggleStyle(.checkbox)
        .disabled(full && !on)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}
