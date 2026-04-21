import SwiftUI

struct ServerCardView: View {
    @Bindable var vm: ServerViewModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss
    @Environment(AppSettings.self) private var settings
    @Environment(NodeStore.self) private var nodeStore

    private var node: Node? { nodeStore.node(withId: vm.id) }

    var body: some View {
        let offline = vm.state.isOffline
        CardChrome(tint: vm.worstTint, offline: offline) {
            header
            subtitle
            metricGrid
        }
        .opacity(offline ? 0.55 : 1.0)
        .contextMenu { contextMenuContent }
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
        if node?.isSnoozed == true {
            Divider()
            Button("Clear snooze") {
                nodeStore.setSnooze(id: vm.id, until: nil)
            }
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
        nodeStore.setSnooze(id: vm.id, until: until)
    }

    private func openFullView(metric: Metric) {
        openWindow(id: "full-view", value: FullViewContext(hostId: vm.id, metric: metric))
        dismiss()
    }

    private var header: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(vm.statusDotColor)
                .frame(width: 8, height: 8)
            Text(vm.hostname)
                .font(Typography.hostname)
                .foregroundStyle(.primary)
            if node?.isSnoozed == true {
                Image(systemName: "bell.slash.fill")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help(snoozeTooltip)
            }
            Spacer(minLength: 4)
            Text(vm.osArch)
                .font(Typography.metaText)
                .foregroundStyle(.secondary)
            Text("·")
                .font(Typography.metaText)
                .foregroundStyle(.secondary)
            Text(lastSeenText)
                .font(Typography.metaText)
                .foregroundStyle(lastSeenColor)
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
            }
            Spacer(minLength: 0)
        }
    }

    private var metricGrid: some View {
        Grid(horizontalSpacing: 8, verticalSpacing: 8) {
            GridRow {
                MetricCell(
                    label: "CPU",
                    series: vm.cpu,
                    mode: .percent,
                    offline: vm.state.isOffline,
                    warn: vm.thresholds.cpuWarn,
                    critical: vm.thresholds.cpuCritical,
                    hoverValue: hoverValue(for: vm.cpu),
                    pollingIntervalSeconds: settings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .cpu) }
                .pointingHandOnHover()
                MetricCell(
                    label: "MEM",
                    series: vm.mem,
                    mode: .percent,
                    offline: vm.state.isOffline,
                    warn: vm.thresholds.memWarn,
                    critical: vm.thresholds.memCritical,
                    hoverValue: hoverValue(for: vm.mem),
                    pollingIntervalSeconds: settings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .mem) }
                .pointingHandOnHover()
            }
            GridRow {
                MetricCell(
                    label: "DISK",
                    series: vm.disk,
                    mode: .diskBars,
                    offline: vm.state.isOffline,
                    warn: vm.thresholds.diskWarn,
                    critical: vm.thresholds.diskCritical,
                    hoverValue: hoverValue(for: vm.disk),
                    pollingIntervalSeconds: settings.pollingInterval(for: vm.kind)
                )
                .onTapGesture { openFullView(metric: .disk) }
                .pointingHandOnHover()
                MetricCell(
                    label: "NET",
                    series: vm.net,
                    mode: .netDualRate,
                    offline: vm.state.isOffline,
                    warn: 0.6,
                    critical: 0.9,
                    rxMBps: vm.netRxMBps,
                    txMBps: vm.netTxMBps,
                    pollingIntervalSeconds: settings.pollingInterval(for: vm.kind)
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

    private var lastSeenText: String {
        guard let seen = vm.lastSeen else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince(seen)))
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
