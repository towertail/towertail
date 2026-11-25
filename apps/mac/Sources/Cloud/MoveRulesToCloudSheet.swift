import SwiftUI

/// Shows the local rules that differ from the cloud and moves the picked ones.
struct MoveRulesToCloudSheet: View {
    let move: CloudRulesMove

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Move your rules to the cloud").font(.system(size: 20, weight: .semibold))
            switch move.phase {
            case .loading:
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Reading the rules of the cloud…").foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            default:
                content
            }
        }
        .padding(24)
        .frame(width: 760, height: 560)
    }

    @ViewBuilder
    private var content: some View {
        if move.rows.isEmpty, case .failed(let message) = move.phase {
            Label(message, systemImage: "exclamationmark.triangle.fill")
                .font(.system(size: 12)).foregroundStyle(.red)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        } else {
            Text("This Mac has its own rules for the org and for your Cloud hosts. After the move, the cloud holds the rules of Cloud hosts. This Mac reads them from the cloud and changes them there. Local SSH hosts keep their rules on this Mac.")
                .font(.system(size: 12)).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            table
            Text("Needs the admin role in the org. The cloud keeps a copy of the old values for 30 days.")
                .font(.system(size: 11)).foregroundStyle(.tertiary)
        }
        if case .failed(let message) = move.phase, !move.rows.isEmpty {
            Label(message, systemImage: "exclamationmark.triangle.fill")
                .font(.system(size: 12)).foregroundStyle(.red)
        }
        buttons
    }

    private var table: some View {
        ScrollView {
            Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 0) {
                GridRow {
                    Color.clear.frame(width: 16, height: 1)
                    header("Scope")
                    header("Rule")
                    header("Cloud now")
                    header("From this Mac")
                }
                .padding(.vertical, 6)
                Divider().gridCellColumns(5)
                ForEach(move.rows) { row in
                    GridRow {
                        Toggle("Move \(row.rule)", isOn: Binding(
                            get: { move.picks.contains(row.id) }, set: { _ in move.toggle(row.id) }))
                            .labelsHidden()
                            .toggleStyle(.checkbox)
                        Text(row.scope.title).font(.system(size: 12, weight: .semibold)).lineLimit(1)
                        Text(row.rule).font(.system(size: 12)).lineLimit(1)
                        Text(row.cloudNow).font(.system(size: 12)).foregroundStyle(.secondary)
                            .strikethrough(true, color: .secondary.opacity(0.5)).lineLimit(1)
                        Text(row.fromMac).font(.system(size: 12, weight: .medium))
                            .foregroundStyle(Color.accentColor).lineLimit(1)
                    }
                    .padding(.vertical, 7)
                    Divider().gridCellColumns(5)
                }
            }
            .padding(.horizontal, 12)
            .disabled(move.phase == .sending)
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func header(_ text: String) -> some View {
        Text(text).font(.system(size: 11, weight: .medium)).foregroundStyle(.secondary)
    }

    private var buttons: some View {
        HStack {
            if move.phase == .sending {
                ProgressView().controlSize(.small)
                Text("Moving \(move.pickedCount) \(move.pickedCount == 1 ? "rule" : "rules")…")
                    .font(.system(size: 12)).foregroundStyle(.secondary)
            }
            Spacer()
            Button("Not now", action: move.close)
                .keyboardShortcut(.cancelAction)
                .disabled(move.phase == .sending)
            if move.rows.isEmpty {
                Button("Try again") { Task { await move.open() } }
                    .keyboardShortcut(.defaultAction)
            } else {
                Button(moveTitle) { Task { await move.start() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(move.pickedCount == 0 || move.phase == .sending)
            }
        }
    }

    private var moveTitle: String {
        let n = move.pickedCount
        return n == 0 ? "Move rules" : "Move \(n) \(n == 1 ? "rule" : "rules")"
    }
}

/// The note after a move. The window shows it for a few seconds.
struct RulesMovedToast: View {
    let text: String

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(ThresholdTint.nominal.color)
            Text(text).font(.system(size: 12))
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(RoundedRectangle(cornerRadius: 10).fill(.regularMaterial))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color.secondary.opacity(0.25)))
        .shadow(radius: 6, y: 2)
    }
}
