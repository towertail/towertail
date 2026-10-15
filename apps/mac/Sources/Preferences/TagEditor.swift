import SwiftUI

/// Tag chips with a remove button each and a field to add one.
struct TagEditor: View {
    @Binding var tags: [String]
    @State private var newTag = ""

    var body: some View {
        FlowLayout(spacing: 5) {
            ForEach(tags, id: \.self) { tag in
                HStack(spacing: 3) {
                    Text(tag)
                    Button {
                        tags.removeAll { $0 == tag }
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 8, weight: .bold))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Remove \(tag)")
                }
                .font(.callout)
                .padding(.horizontal, 7)
                .padding(.vertical, 2)
                .background(Capsule().fill(Color.accentColor.opacity(0.15)))
                .foregroundStyle(Color.accentColor)
            }
            TextField("Tag", text: $newTag, prompt: Text("Add tag"))
                .labelsHidden()
                .textFieldStyle(.plain)
                .font(.callout)
                .frame(width: 80)
                .onSubmit(add)
        }
    }

    private func add() {
        let t = newTag.trimmingCharacters(in: .whitespaces)
        newTag = ""
        guard !t.isEmpty, !tags.contains(t) else { return }
        tags.append(t)
    }
}

/// Lays out children left to right and wraps to a new line when full.
private struct FlowLayout: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let width = rows.map { $0.width }.max() ?? 0
        let height = rows.map { $0.height }.reduce(0, +) + spacing * CGFloat(max(0, rows.count - 1))
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for i in row.indices {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y + (row.height - size.height) / 2), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row {
        var indices: [Int] = []
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = [Row()]
        for i in subviews.indices {
            let size = subviews[i].sizeThatFits(.unspecified)
            let extra = rows[rows.count - 1].indices.isEmpty ? size.width : size.width + spacing
            if rows[rows.count - 1].width + extra > width, !rows[rows.count - 1].indices.isEmpty {
                rows.append(Row())
            }
            let isFirst = rows[rows.count - 1].indices.isEmpty
            rows[rows.count - 1].indices.append(i)
            rows[rows.count - 1].width += isFirst ? size.width : size.width + spacing
            rows[rows.count - 1].height = max(rows[rows.count - 1].height, size.height)
        }
        return rows
    }
}
