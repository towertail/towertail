import SwiftUI
import AppKit

struct PopoverHeader: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    /// Used to grab focus when the popover reopens. `MenuBarExtra` keeps
    /// the view tree alive across dismissals, so `.onAppear` only fires
    /// once — we have to react to the scene-phase transition instead.
    @Environment(\.scenePhase) private var scenePhase
    @Binding var filter: PopoverFilter
    /// Live, un-debounced query text. The popover owns the debounced copy
    /// used for filtering — the header just drives the text field.
    @Binding var searchText: String
    @FocusState private var searchFocused: Bool

    var body: some View {
        let s = store.summary
        VStack(spacing: 6) {
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
                    warnPill(warn: s.warn, critical: s.critical)
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
            searchField
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Color.black.opacity(0.001))
        .onChange(of: scenePhase) { _, phase in
            // Popover just became visible — autofocus the filter so the
            // user can type to narrow the list without having to click
            // first. Skip when the popover is going away or staying away.
            guard phase == .active else { return }
            searchFocused = true
        }
        .onAppear {
            // First open after launch: scenePhase may already be .active
            // by the time the body mounts, so onChange wouldn't fire.
            searchFocused = true
        }
    }

    private var searchField: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .font(.caption)
                .foregroundStyle(.secondary)
            TextField("Filter servers", text: $searchText)
                .textFieldStyle(.plain)
                .font(Typography.metaText)
                .focused($searchFocused)
                .onExitCommand { searchText = "" }
                .onSubmit { searchFocused = false }
            if !searchText.isEmpty {
                Button {
                    searchText = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .help("Clear filter")
                .pointingHandOnHover()
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(Color.secondary.opacity(0.12))
        )
    }

    private var sep: some View {
        Text("·").foregroundStyle(.secondary)
    }

    /// Combined warn/critical pill. When any host is critical the pill
    /// switches to the critical tint and shows "{critical}/{warn}" so the
    /// more-severe count leads. With no criticals it behaves identically
    /// to the plain warn pill.
    @ViewBuilder
    private func warnPill(warn: Int, critical: Int) -> some View {
        let active = filter == .warn
        let hasCritical = critical > 0
        let hasAny = hasCritical || warn > 0
        let tint = hasCritical ? ThresholdTint.critical.color : ThresholdTint.warn.color
        let label: String = {
            if hasCritical {
                return "\(critical) critical"
            }
            return "\(warn) warn"
        }()
        Button {
            filter = active ? .all : .warn
        } label: {
            Text(label)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(
                    RoundedRectangle(cornerRadius: 6)
                        .fill(active ? tint.opacity(0.22) : .clear)
                )
                .foregroundStyle(hasAny ? tint : Color.secondary)
        }
        .buttonStyle(.plain)
        .help(warnPillHelp(active: active, critical: critical, warn: warn))
        .pointingHandOnHover()
    }

    private func warnPillHelp(active: Bool, critical: Int, warn: Int) -> String {
        if active { return "Show all hosts" }
        if critical > 0 {
            return "Show \(critical) critical and \(warn) warn host\(warn == 1 ? "" : "s")"
        }
        return "Show only hosts in warn or critical"
    }

    /// Clickable pill. Click once to filter, click again to clear. Active
    /// filter uses a subtle tint-colored background so the state is obvious
    /// without shouting; inactive sits in secondary text color to match the
    /// old summary line.
    @ViewBuilder
    private func pill(kind: PopoverFilter, count: Int) -> some View {
        let active = filter == kind
        let tint = pillTint(kind: kind)
        let tinted = count > 0
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
                .foregroundStyle(tinted ? tint : Color.secondary)
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
