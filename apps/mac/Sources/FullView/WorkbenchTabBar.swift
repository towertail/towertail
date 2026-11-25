import SwiftUI

/// The tabs, the range control and the live badge.
struct WorkbenchTabBar: View {
    @Bindable var model: FullViewModel
    let lastSample: Date?
    /// Oldest time that the host has data for. Nil enables no range.
    var oldest: Date? = nil
    /// Cloud hosts have the history on the server, so all ranges work.
    var allRanges = false

    var body: some View {
        HStack(spacing: 12) {
            WBSegments(items: HostTab.allCases, selection: $model.tab, label: \.displayName, uppercase: true)
            Spacer(minLength: 8)
            if let z = model.zoomRange {
                Button {
                    model.resetZoom()
                } label: {
                    Text("Reset zoom · \(z.lowerBound.formatted(date: .omitted, time: .shortened)) → \(z.upperBound.formatted(date: .omitted, time: .shortened))")
                        .font(.system(size: 12, design: .monospaced))
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .help("Return to the range")
            }
            WBSegments(
                items: HostRange.allCases,
                selection: Binding(get: { model.range }, set: { model.range = $0 }),
                label: \.rawValue,
                isEnabled: { allRanges || model.isAvailable($0, oldest: oldest, now: Date()) }
            )
            liveBadge
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
    }

    @ViewBuilder
    private var liveBadge: some View {
        if case .pinned(let t) = model.mode {
            Button {
                model.unpin()
            } label: {
                Text("◆ Pinned \(t.formatted(date: .omitted, time: .standard)) · Esc")
            }
            .buttonStyle(Badge(color: WB.pin))
            .help("Back to live")
        } else {
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                let age = lastSample.map { max(0, Int(ctx.date.timeIntervalSince($0))) }
                HStack(spacing: 6) {
                    Circle().fill(WB.cpu).frame(width: 7, height: 7)
                    Text(age.map { "Live · updated \($0) s ago" } ?? "Live")
                }
                .modifier(BadgeLook(color: WB.muted))
            }
        }
    }

    private struct Badge: ButtonStyle {
        let color: Color
        func makeBody(configuration: Configuration) -> some View {
            configuration.label.modifier(BadgeLook(color: color)).opacity(configuration.isPressed ? 0.7 : 1)
        }
    }

    private struct BadgeLook: ViewModifier {
        let color: Color
        func body(content: Content) -> some View {
            content
                .font(.system(size: 12.5, design: .monospaced))
                .lineLimit(1).fixedSize()
                .foregroundStyle(color)
                .padding(.horizontal, 10).padding(.vertical, 4)
                .background(WB.surface, in: Capsule())
                .overlay(Capsule().stroke(color == WB.muted ? WB.border : color, lineWidth: 1))
        }
    }
}
