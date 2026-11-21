import SwiftUI

struct NetSplit: Equatable {
    let rx: Double
    let tx: Double
}

struct RowMetric: Equatable {
    let metric: Metric
    let text: String
    let fraction: Double
    let tint: ThresholdTint
    var split: NetSplit? = nil
}

enum RowState: Equatable {
    case live
    case noSample(text: String, alerted: Bool)
}

enum RowsLayout {
    static let valueWidth: CGFloat = 46
    static let netWidth: CGFloat = 52
    static let gap: CGFloat = 8
    static let inset: CGFloat = 14
    static let minHeight: CGFloat = 48
    static let netRx = Color.blue
    static let netTx = Color(.sRGB, red: 0xBF / 255, green: 0x5A / 255, blue: 0xF2 / 255)
}

struct ServerRowView: View {
    let vm: ServerViewModel
    @Environment(NodeStore.self) private var nodeStore
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismiss) private var dismiss

    private var name: String { nodeStore.node(withId: vm.id)?.displayName ?? vm.hostname }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 15)) { ctx in
            let state = Self.state(for: vm, now: ctx.date)
            Button(action: open) {
                HStack(spacing: RowsLayout.gap) {
                    nameColumn
                    switch state {
                    case .live:
                        ForEach(Array(Self.metrics(for: vm).enumerated()), id: \.offset) { i, m in
                            MetricColumn(metric: m, hostId: vm.id)
                                .frame(width: i == 3 ? RowsLayout.netWidth : RowsLayout.valueWidth)
                        }
                    case .noSample(let text, _):
                        Text(text)
                            .font(.system(size: 11.5))
                            .foregroundStyle(noSampleColor(state))
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(.horizontal, RowsLayout.inset)
                .padding(.vertical, 8)
                .frame(minHeight: RowsLayout.minHeight)
                .background(rowBackground(state))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Self.accessibilityLabel(name: name, state: state, metrics: Self.metrics(for: vm)))
            .accessibilityAddTraits(.isButton)
            .pointingHandOnHover()
        }
    }

    /// The tag sits under the name: the name column is too narrow for both
    /// on one line.
    private var nameColumn: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 7) {
                Circle()
                    .fill(vm.statusDotColor)
                    .frame(width: 7, height: 7)
                Text(name)
                    .font(Typography.rowName)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            if let tag = HostTag.tag(forNode: vm.id, in: nodeStore, cloud: cloud) {
                HostTagView(tag: tag)
                    .padding(.leading, 14)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func rowBackground(_ state: RowState) -> Color {
        if case .noSample(_, true) = state { return ThresholdTint.critical.color.opacity(0.12) }
        return .clear
    }

    private func noSampleColor(_ state: RowState) -> Color {
        if case .noSample(_, true) = state { return ThresholdTint.critical.color }
        return .secondary
    }

    private func open() {
        openWindow(id: "full-view", value: FullViewContext(hostId: vm.id, metric: .health))
        dismiss()
        ActivationPolicyCoordinator.shared.bringToFront()
    }
}

extension ServerRowView {
    /// CPU, MEM, DISK and NET, in column order.
    static func metrics(for vm: ServerViewModel) -> [RowMetric] {
        let t = vm.thresholds
        return [
            percent(vm.cpu, metric: .cpu, warn: t.cpuWarn, critical: t.cpuCritical),
            percent(vm.mem, metric: .mem, warn: t.memWarn, critical: t.memCritical),
            percent(vm.disk, metric: .disk, warn: t.diskWarn, critical: t.diskCritical),
            net(rx: vm.netRxMBps, tx: vm.netTxMBps),
        ]
    }

    static func state(for vm: ServerViewModel, now: Date) -> RowState {
        guard vm.state.isOffline else { return .live }
        let age = ShortDuration.text(now.timeIntervalSince(vm.lastIngestAt ?? vm.staleBaseline))
        if vm.everConnected {
            return .noSample(text: "No sample \(age) · alerted", alerted: true)
        }
        return .noSample(text: "Not connected", alerted: false)
    }

    static func accessibilityLabel(name: String, state: RowState, metrics: [RowMetric]) -> String {
        if case .noSample(let text, _) = state { return "\(name), \(text)" }
        guard metrics.count == 4 else { return name }
        let net = metrics[3].text.replacingOccurrences(of: " MB/s", with: " megabytes per second")
        return "\(name), CPU \(percentWords(metrics[0])), memory \(percentWords(metrics[1])), "
            + "disk \(percentWords(metrics[2])), network \(net)"
    }

    private static func percentWords(_ m: RowMetric) -> String {
        m.text.replacingOccurrences(of: "%", with: " percent")
    }

    private static func percent(_ series: MetricSeries, metric: Metric, warn: Double, critical: Double) -> RowMetric {
        guard let v = series.latest?.v else { return RowMetric(metric: metric, text: "—", fraction: 0, tint: .stale) }
        return RowMetric(
            metric: metric,
            text: "\(Int(round(v * 100)))%",
            fraction: min(max(v, 0), 1),
            tint: series.tint(warn: warn, critical: critical)
        )
    }

    private static func net(rx: Double, tx: Double) -> RowMetric {
        let total = rx + tx
        let text = String(format: total < 10 ? "%.1f MB/s" : "%.0f MB/s", total)
        let split = total > 0 ? NetSplit(rx: rx / total, tx: tx / total) : NetSplit(rx: 0, tx: 0)
        return RowMetric(metric: .net, text: text, fraction: total > 0 ? 1 : 0, tint: .nominal, split: split)
    }
}

private struct MetricColumn: View {
    let metric: RowMetric
    let hostId: UUID
    @Environment(MetricHoverState.self) private var hover: MetricHoverState?

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(metric.text)
                .font(Typography.rowValue)
                .foregroundStyle(valueColor)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            bar
        }
        .background { hoverZone }
    }

    /// A zone a little larger than the column. It reports its frame at hover time.
    @ViewBuilder
    private var hoverZone: some View {
        if MetricHoverContent.supports(metric.metric) {
            GeometryReader { g in
                Color.clear
                    .contentShape(Rectangle())
                    .onHover { inside in
                        let target = MetricHoverTarget(
                            hostId: hostId, metric: metric.metric,
                            anchor: g.frame(in: .named(MetricHoverState.space))
                        )
                        if inside { hover?.enter(target) } else { hover?.exit(target) }
                    }
            }
            .padding(.horizontal, -4)
            .padding(.vertical, -6)
        }
    }

    private var valueColor: Color {
        switch metric.tint {
        case .warn, .critical: metric.tint.color
        case .nominal, .stale: .primary
        }
    }

    private var bar: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Color.secondary.opacity(0.25))
                if let split = metric.split {
                    HStack(spacing: 0) {
                        Rectangle().fill(RowsLayout.netRx).frame(width: g.size.width * split.rx)
                        Rectangle().fill(RowsLayout.netTx).frame(width: g.size.width * split.tx)
                    }
                    .clipShape(Capsule())
                } else {
                    Capsule()
                        .fill(metric.tint.color)
                        .frame(width: g.size.width * metric.fraction)
                }
            }
        }
        .frame(height: 3)
    }
}

struct ServerRowsHeader: View {
    let count: Int

    var body: some View {
        HStack(spacing: RowsLayout.gap) {
            Text("SERVER · \(count)")
                .frame(maxWidth: .infinity, alignment: .leading)
            Text("CPU").frame(width: RowsLayout.valueWidth, alignment: .leading)
            Text("MEM").frame(width: RowsLayout.valueWidth, alignment: .leading)
            Text("DISK").frame(width: RowsLayout.valueWidth, alignment: .leading)
            Text("NET").frame(width: RowsLayout.netWidth, alignment: .leading)
        }
        .font(Typography.rowColumn)
        .tracking(1)
        .foregroundStyle(.secondary)
        .padding(.horizontal, RowsLayout.inset)
        .padding(.top, 8)
        .padding(.bottom, 6)
    }
}
