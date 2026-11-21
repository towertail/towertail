import SwiftUI

/// The info box, the counter and one checkbox for each host. A picked host goes to Cloud.
struct CloudPickList: View {
    let hosts: [Node]
    let picks: CloudPicks
    let note: String
    let toggle: (UUID) -> Void
    let clear: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            info
            HStack {
                Text(picks.counter).font(.system(size: 12, weight: .medium))
                Button("Clear", action: clear)
                    .buttonStyle(.link)
                    .disabled(picks.ids.isEmpty)
                Spacer()
            }
            list
        }
    }

    private var info: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "info.circle").foregroundStyle(Color.accentColor)
            Text(note).font(.system(size: 12)).fixedSize(horizontal: false, vertical: true)
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
                ForEach(hosts) { node in
                    row(node)
                    if node.id != hosts.last?.id { Divider() }
                }
            }
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func row(_ node: Node) -> some View {
        let on = picks.ids.contains(node.id)
        return Toggle(isOn: Binding(get: { on }, set: { _ in toggle(node.id) })) {
            HStack {
                Text(node.displayName).font(Typography.rowName).lineLimit(1)
                Spacer()
                Text(on ? "Cloud: 24/7 alerts and history" : "SSH: live view on this Mac")
                    .font(.system(size: 12))
                    .foregroundStyle(on ? ThresholdTint.nominal.color : Color.secondary)
            }
        }
        .toggleStyle(.checkbox)
        .disabled(picks.isFull && !on)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}
