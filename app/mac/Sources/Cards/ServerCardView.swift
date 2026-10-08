import SwiftUI

struct ServerCardView: View {
    @Bindable var vm: ServerViewModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss
    @Environment(ClientSettings.self) private var clientSettings
    @Environment(ServerSettings.self) private var serverSettings
    @Environment(NodeStore.self) private var nodeStore
    @Environment(SamplerUpdateCoordinator.self) private var samplerUpdater
    @Environment(\.backend) private var backend
    @State private var terminalError: String?

    /// Per-render bundle of node metadata + backend mutations. Re-derived
    /// each body invocation so changes to NodeStore (favorite toggled
    /// elsewhere, snooze cleared) flow through naturally.
    private var model: ServerCardModel {
        ServerCardModel(
            nodeID: vm.id,
            nodeStore: nodeStore,
            samplerUpdater: samplerUpdater,
            backend: backend
        )
    }

    private var node: Node? { model.node }

    var body: some View {
        let offline = vm.state.isOffline
        let suspended = vm.state.isSuspended
        // Suspended dims the card (we don't have live data) but visually
        // reads as "paused" rather than "down" — no red, lower opacity
        // than offline so the user can tell at a glance which is which.
        CardChrome(tint: vm.worstTint, offline: offline || suspended) {
            header
            subtitle
            metricGrid
            if !offline && !suspended, let summary = vm.health.summary {
                healthLine(summary)
            }
        }
        .opacity(offline ? 0.55 : (suspended ? 0.7 : 1.0))
        .contextMenu { contextMenuContent }
        .alert("Couldn't open terminal", isPresented: Binding(
            get: { terminalError != nil },
            set: { if !$0 { terminalError = nil } }
        )) {
            Button("OK") { terminalError = nil }
        } message: {
            Text(terminalError ?? "")
        }
    }

    @ViewBuilder
    private var contextMenuContent: some View {
        Menu("Snooze notifications") {
            Button("15 minutes") { snooze(.minutes(15)) }
            Button("1 hour") { snooze(.minutes(60)) }
            Button("4 hours") { snooze(.minutes(240)) }
            Button("Until tomorrow 9am") { snooze(.untilTomorrow9am) }
            Button("1 day") { snooze(.minutes(24 * 60)) }
            Button("1 week") { snooze(.minutes(7 * 24 * 60)) }
        }
        if model.isSnoozed {
            Divider()
            Button("Clear snooze") { model.snooze(id: vm.id, until: nil) }
        }
    }

    private enum SnoozePreset {
        case minutes(Int)
        case untilTomorrow9am
    }

    private func snooze(_ preset: SnoozePreset) {
        let until: Date
        switch preset {
        case .minutes(let m):
            until = Date().addingTimeInterval(TimeInterval(m * 60))
        case .untilTomorrow9am:
            // Next occurrence of 09:00 local. If it's already past 09:00
            // today, that's tomorrow; if it's before, that's still today —
            // "until tomorrow 9am" means the next 9am that's at least a few
            // hours out, so we always advance by one day from today's 9am.
            let cal = Calendar.current
            let now = Date()
            var comps = cal.dateComponents([.year, .month, .day], from: now)
            comps.hour = 9
            comps.minute = 0
            let todayAt9 = cal.date(from: comps) ?? now
            until = cal.date(byAdding: .day, value: 1, to: todayAt9) ?? now.addingTimeInterval(24 * 3600)
        }
        model.snooze(id: vm.id, until: until)
    }

    private func openFullView(metric: Metric) {
        openWindow(id: "full-view", value: FullViewContext(hostId: vm.id, metric: metric))
        dismiss()
        ActivationPolicyCoordinator.shared.bringToFront()
    }

    /// One compact line for host health problems. Opens the HEALTH tab.
    private func healthLine(_ summary: String) -> some View {
        Button { openFullView(metric: .health) } label: {
            Label(summary, systemImage: "stethoscope")
                .font(Typography.metaText)
                .foregroundStyle(vm.health.tint.color)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.plain)
        .help(vm.health.reasons.map(\.text).joined(separator: "\n"))
    }

    private var header: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(vm.statusDotColor)
                .frame(width: 8, height: 8)
            Text(node?.displayName ?? vm.hostname)
                .font(Typography.hostname)
                .foregroundStyle(.primary)
            if model.isSnoozed {
                Image(systemName: "bell.slash.fill")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help(snoozeTooltip)
            }
            if model.isUpdatingSampler {
                HStack(spacing: 4) {
                    ProgressView()
                        .controlSize(.mini)
                    Text("Updating sampler…")
                        .font(Typography.metaText)
                        .foregroundStyle(.secondary)
                }
                .help("Pushing the bundled sampler binary because the remote version doesn't match this app build.")
            }
            Spacer(minLength: 4)
            Text(vm.osArch)
                .font(Typography.metaText)
                .foregroundStyle(.secondary)
            Text("·")
                .font(Typography.metaText)
                .foregroundStyle(.secondary)
            // TimelineView isolates the 1Hz rebuild to just this label —
            // the rest of the card (charts, metric grid) stays static
            // between real sample ingestions. Also pauses when the popover
            // is closed because off-screen TimelineViews don't schedule.
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                Text(lastSeenText(now: ctx.date))
                    .font(Typography.metaText)
                    .foregroundStyle(lastSeenColor)
            }
        }
    }

    @ViewBuilder
    private var headerActions: some View {
        HStack(spacing: 10) {
            if let n = node, n.kind == .ssh {
                Button {
                    openSSHTerminal(for: n)
                } label: {
                    Image(systemName: "terminal")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .help("Open SSH session in \(clientSettings.defaultTerminalApp)")
                .pointingHandOnHover()
            }
            Button {
                if let n = node {
                    openWindow(id: "server-edit", value: n.id)
                    ActivationPolicyCoordinator.shared.bringToFront()
                }
            } label: {
                Image(systemName: "slider.horizontal.3")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .help("Settings for this server")
            .pointingHandOnHover()
            .disabled(node == nil)
            Button {
                if let n = node {
                    model.toggleFavorite(id: n.id, currentlyFavorite: n.favorite)
                }
            } label: {
                Image(systemName: model.isFavorite ? "star.fill" : "star")
                    .font(.caption)
                    .foregroundStyle(model.isFavorite ? Color.yellow : .secondary)
            }
            .buttonStyle(.plain)
            .help(model.isFavorite ? "Unpin favorite" : "Pin as favorite")
            .pointingHandOnHover()
            .disabled(node == nil)
        }
    }

    private func openSSHTerminal(for n: Node) {
        let result = TerminalLauncher.openSSH(for: n, app: clientSettings.defaultTerminalApp)
        if case .failure(let err) = result {
            terminalError = err.localizedDescription
        } else {
            dismiss()
        }
    }

    private var snoozeTooltip: String {
        guard let until = node?.snoozedUntil else { return "Snoozed" }
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .full
        return "Notifications snoozed \(formatter.localizedString(for: until, relativeTo: Date()))"
    }

    private var subtitle: some View {
        HStack(spacing: 4) {
            Text(vm.dnsName)
                .font(Typography.subtitle)
                .foregroundStyle(.secondary)
            if case .offline(let reason) = vm.state {
                Text("·")
                    .font(Typography.subtitle)
                    .foregroundStyle(.secondary)
                Text(reason)
                    .font(Typography.subtitle)
                    .foregroundStyle(ThresholdTint.critical.color)
            } else if case .suspended(let reason) = vm.state {
                Text("·")
                    .font(Typography.subtitle)
                    .foregroundStyle(.secondary)
                Text("paused — \(reason)")
                    .font(Typography.subtitle)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
            headerActions
        }
    }

    private var metricGrid: some View {
        Grid(horizontalSpacing: 8, verticalSpacing: 8) {
            GridRow {
                MetricCell(
                    label: "CPU",
                    series: vm.cpu,
                    mode: .percent,
                    offline: vm.state.isOffline || vm.state.isSuspended,
                    warn: vm.thresholds.cpuWarn,
                    critical: vm.thresholds.cpuCritical,
                    hoverValue: hoverValue(for: vm.cpu),
                    pollingIntervalSeconds: serverSettings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .cpu) }
                .pointingHandOnHover()
                MetricCell(
                    label: "MEM",
                    series: vm.mem,
                    mode: .percent,
                    offline: vm.state.isOffline || vm.state.isSuspended,
                    warn: vm.thresholds.memWarn,
                    critical: vm.thresholds.memCritical,
                    hoverValue: hoverValue(for: vm.mem),
                    pollingIntervalSeconds: serverSettings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .mem) }
                .pointingHandOnHover()
            }
            GridRow {
                MetricCell(
                    label: "DISK",
                    series: vm.disk,
                    mode: .diskBars,
                    offline: vm.state.isOffline || vm.state.isSuspended,
                    warn: vm.thresholds.diskWarn,
                    critical: vm.thresholds.diskCritical,
                    hoverValue: hoverValue(for: vm.disk),
                    pollingIntervalSeconds: serverSettings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .disk) }
                .pointingHandOnHover()
                MetricCell(
                    label: "NET",
                    series: vm.net,
                    mode: .netDualRate,
                    offline: vm.state.isOffline || vm.state.isSuspended,
                    warn: 0.6,
                    critical: 0.9,
                    rxMBps: vm.netRxMBps,
                    txMBps: vm.netTxMBps,
                    pollingIntervalSeconds: serverSettings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .net) }
                .pointingHandOnHover()
            }
        }
    }

    private func hoverValue(for series: MetricSeries) -> Double? {
        guard let h = vm.hoverDate else { return nil }
        return series.nearest(to: h)?.v
    }

    private func lastSeenText(now: Date) -> String {
        guard let seen = vm.lastSeen else { return "—" }
        let secs = max(0, Int(now.timeIntervalSince(seen)))
        if secs < 60 { return "\(secs)s" }
        let mins = secs / 60
        if mins < 60 { return "\(mins)m ago" }
        let hrs = mins / 60
        return "\(hrs)h ago"
    }

    private var lastSeenColor: Color {
        if vm.state.isOffline { return ThresholdTint.critical.color }
        return .secondary
    }
}
