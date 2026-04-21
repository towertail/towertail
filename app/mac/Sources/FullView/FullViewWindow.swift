import SwiftUI

struct FullViewWindow: View {
    let context: FullViewContext
    @Environment(ServerStore.self) private var store
    @Environment(AppSettings.self) private var settings
    @State private var model: FullViewModel

    init(context: FullViewContext) {
        self.context = context
        _model = State(initialValue: FullViewModel(metric: context.metric))
    }

    var body: some View {
        let vm = store.serverVMs.first(where: { $0.id == context.hostId })
        VStack(spacing: 0) {
            header(vm: vm)
            Divider()
            content(vm: vm)
            Divider()
            toolbar(vm: vm)
        }
        .frame(minWidth: 800, minHeight: 500)
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.space) {
            guard let vm else { return .ignored }
            let latest = currentSeries(vm: vm).latest?.t
            model.togglePlayPause(latest: latest)
            return .handled
        }
    }

    @ViewBuilder
    private func header(vm: ServerViewModel?) -> some View {
        HStack(spacing: 10) {
            Text(vm?.hostname ?? "Unknown host")
                .font(.title3).bold()
            Text(vm?.dnsName ?? "")
                .font(.callout)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
    }

    @ViewBuilder
    private func content(vm: ServerViewModel?) -> some View {
        VStack(spacing: 12) {
            Picker("Metric", selection: $model.metric) {
                ForEach(Metric.allCases, id: \.self) { m in
                    Text(m.displayName).tag(m)
                }
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 16)

            if let vm {
                chartArea(vm: vm)
                emptyProcessTableStub
            } else {
                ContentUnavailableView("No data", systemImage: "questionmark.circle")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .padding(.vertical, 10)
    }

    @ViewBuilder
    private func chartArea(vm: ServerViewModel) -> some View {
        let series = currentSeries(vm: vm)
        let (warn, critical) = thresholds(for: model.metric, vm: vm)
        let latest = series.latest?.t
        let pinned: Date? = {
            if case .pinned(let t) = model.mode { return t }
            return nil
        }()

        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 12) {
                Text(model.metric.displayName)
                    .font(.headline)
                valueText(vm: vm)
                if case .pinned = model.mode {
                    Label("Pinned", systemImage: "pin.fill")
                        .font(.caption)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Color.orange.opacity(0.2), in: Capsule())
                        .foregroundStyle(Color.orange)
                }
                Spacer()
                Text(snapshotAgeText(latest: latest))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 16)

            MetricChart(
                samples: series.snapshot(),
                tint: tint(vm: vm, series: series, metric: model.metric),
                warn: warn,
                critical: critical,
                hoverAt: model.hoverAt,
                pinnedAt: pinned,
                onHover: { t in
                    model.hoverAt = t
                },
                onPinTap: { t in
                    model.pin(at: t)
                }
            )
            .frame(minHeight: 240)
            .padding(.horizontal, 16)
        }
    }

    private var emptyProcessTableStub: some View {
        VStack(spacing: 6) {
            Image(systemName: "list.bullet.rectangle")
                .font(.title2)
                .foregroundStyle(.secondary)
            Text("Per-process metrics land in v2")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 120)
        .padding(8)
        .background(Color.secondary.opacity(0.05))
        .padding(.horizontal, 16)
    }

    @ViewBuilder
    private func toolbar(vm: ServerViewModel?) -> some View {
        HStack(spacing: 10) {
            Button {
                let latest = vm.map { currentSeries(vm: $0).latest?.t } ?? nil
                model.togglePlayPause(latest: latest)
            } label: {
                Image(systemName: model.isLive ? "pause.fill" : "play.fill")
            }
            .keyboardShortcut(" ", modifiers: [])

            Button {
                // v1.1 step-back
            } label: {
                Image(systemName: "backward.frame")
            }
            .disabled(model.isLive)

            Button {
                // v1.1 step-forward
            } label: {
                Image(systemName: "forward.frame")
            }
            .disabled(model.isLive)

            if case .pinned = model.mode {
                Button("Unpin") { model.unpin() }
            }

            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
    }

    private func valueText(vm: ServerViewModel) -> some View {
        let series = currentSeries(vm: vm)
        let effective = model.effectiveTimestamp(latest: series.latest?.t)
        let point = effective.flatMap { series.nearest(to: $0) } ?? series.latest
        let v = point?.v ?? 0
        let text: String
        switch model.metric {
        case .net:
            text = String(format: "↓%.1f MB/s · ↑%.1f MB/s", vm.netRxMBps, vm.netTxMBps)
        default:
            text = "\(Int(round(v * 100)))%"
        }
        return Text(text)
            .font(.system(.title3, design: .rounded).monospacedDigit())
    }

    private func currentSeries(vm: ServerViewModel) -> MetricSeries {
        switch model.metric {
        case .cpu: return vm.cpu
        case .mem: return vm.mem
        case .disk: return vm.disk
        case .net: return vm.net
        }
    }

    private func thresholds(for metric: Metric, vm: ServerViewModel) -> (Double?, Double?) {
        switch metric {
        case .cpu: return (vm.thresholds.cpuWarn, vm.thresholds.cpuCritical)
        case .mem: return (vm.thresholds.memWarn, vm.thresholds.memCritical)
        case .disk: return (vm.thresholds.diskWarn, vm.thresholds.diskCritical)
        case .net: return (nil, nil)
        }
    }

    private func tint(vm: ServerViewModel, series: MetricSeries, metric: Metric) -> Color {
        let (warn, critical) = thresholds(for: metric, vm: vm)
        if let w = warn, let c = critical {
            return series.tint(warn: w, critical: c).color
        }
        return ThresholdTint.nominal.color
    }

    private func snapshotAgeText(latest: Date?) -> String {
        guard let latest else { return "—" }
        let age = Int(max(0, Date().timeIntervalSince(latest)))
        if age < 60 { return "\(age)s ago" }
        return "\(age / 60)m ago"
    }
}
