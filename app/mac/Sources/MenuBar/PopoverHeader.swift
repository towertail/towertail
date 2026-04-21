import SwiftUI
import AppKit

struct PopoverHeader: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @Binding var filter: PopoverFilter

    var body: some View {
        let s = store.summary
        HStack(spacing: 6) {
            Text("Towertail")
                .font(Typography.headerTitle)
                .layoutPriority(0)
            Spacer(minLength: 6)
            // Pills are the primary content of the header — give them
            // priority so `N online` doesn't get truncated when the title
            // + chrome on either side competes for width.
            HStack(spacing: 2) {
                pill(kind: .online, count: s.online)
                sep
                pill(kind: .warn, count: s.warn)
                sep
                pill(kind: .down, count: s.down)
            }
            .font(Typography.metaText)
            .lineLimit(1)
            .fixedSize(horizontal: true, vertical: false)
            .layoutPriority(1)
            Spacer(minLength: 6)
            SettingsLink {
                Image(systemName: "gearshape")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .help("Settings")
            .simultaneousGesture(TapGesture().onEnded {
                NSApp.activate(ignoringOtherApps: true)
                // Auto-hide the menu-bar popover (same behavior as tapping a
                // chart card). Without this the popover stays pinned over
                // Settings until the user clicks away.
                dismiss()
            })
            Button {
                NSApp.terminate(nil)
            } label: {
                Image(systemName: "power")
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .help("Quit Towertail")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(height: 36)
        .background(Color.black.opacity(0.001))
    }

    private var sep: some View {
        Text("·").foregroundStyle(.secondary)
    }

    /// Clickable pill. Click once to filter, click again to clear. Active
    /// filter uses a subtle tint-colored background so the state is obvious
    /// without shouting; inactive sits in secondary text color to match the
    /// old summary line.
    @ViewBuilder
    private func pill(kind: PopoverFilter, count: Int) -> some View {
        let active = filter == kind
        let tint = pillTint(kind: kind)
        Button {
            filter = active ? .all : kind
        } label: {
            Text("\(count) \(pillLabel(kind: kind))")
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(
                    RoundedRectangle(cornerRadius: 6)
                        .fill(active ? tint.opacity(0.22) : .clear)
                )
                .foregroundStyle(active ? tint : Color.secondary)
        }
        .buttonStyle(.plain)
        .help(helpText(kind: kind, active: active))
        .pointingHandOnHover()
    }

    private func pillLabel(kind: PopoverFilter) -> String {
        switch kind {
        case .online: return "online"
        case .warn: return "warn"
        case .down: return "down"
        case .all: return ""
        }
    }

    private func pillTint(kind: PopoverFilter) -> Color {
        switch kind {
        case .online: return ThresholdTint.nominal.color
        case .warn: return ThresholdTint.warn.color
        case .down: return ThresholdTint.critical.color
        case .all: return .secondary
        }
    }

    private func helpText(kind: PopoverFilter, active: Bool) -> String {
        let subject: String = {
            switch kind {
            case .online: return "online hosts"
            case .warn: return "hosts in warn or critical"
            case .down: return "offline hosts"
            case .all: return ""
            }
        }()
        return active ? "Show all hosts" : "Show only \(subject)"
    }
}
