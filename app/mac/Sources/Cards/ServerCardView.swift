import SwiftUI

struct ServerCardView: View {
    @Bindable var vm: ServerViewModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss
    @Environment(AppSettings.self) private var settings

    var body: some View {
        let offline = vm.state.isOffline
        CardChrome(tint: vm.worstTint, offline: offline) {
            header
            subtitle
            metricGrid
        }
        .opacity(offline ? 0.55 : 1.0)
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
