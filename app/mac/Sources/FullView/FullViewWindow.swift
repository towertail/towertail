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
        // Baseline layout with the DISK tab as the tallest worst case:
        // header (~40) + picker (~36) + capacity chart (min 90) +
        // i/o chart (min 90) + process table (min 160) + toolbar (~40) +
        // paddings/dividers ≈ 540. Keep a little breathing room above
        // that minimum so the window doesn't feel squished on first open,
        // but let users shrink below 720 without chopping the toolbar.
        .frame(minWidth: 860, minHeight: 560)
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.space) {
            guard let vm else { return .ignored }
            let latest = currentSeries(vm: vm).latest?.t
            model.togglePlayPause(latest: latest)
            return .handled
        }
        .onAppear { ActivationPolicyCoordinator.shared.acquire() }
        .onDisappear { ActivationPolicyCoordinator.shared.release() }
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
                if model.metric == .disk {
                    diskArea(vm: vm)
                } else {
                    chartArea(vm: vm)
                }
                processTable(vm: vm)
            } else {
                ContentUnavailableView("No data", systemImage: "questionmark.circle")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .padding(.vertical, 10)
    }

    /// DISK tab: two stacked charts (capacity + I/O) with per-mount /
    /// per-device pickers. The capacity chart preserves the existing
    /// "worst mount" default so opening DISK looks the same as before.
    @ViewBuilder
    private func diskArea(vm: ServerViewModel) -> some View {
        VStack(spacing: 12) {
            capacityChart(vm: vm)
            Divider().padding(.horizontal, 16)
            ioChart(vm: vm)
        }
    }

    @ViewBuilder
    private func capacityChart(vm: ServerViewModel) -> some View {
        let series: MetricSeries = {
            switch model.diskMount {
            case .max: return vm.disksPerMount.max
            case .mount(let m): return vm.disksPerMount.series(forMount: m) ?? vm.disksPerMount.max
            }
        }()
        let (warn, critical) = thresholds(for: .disk, vm: vm)
        let latest = series.latest?.t
        let pinned: Date? = {
            if case .pinned(let t) = model.mode { return t }
            return nil
        }()
        let effective = model.effectiveTimestamp(latest: latest)
        let point = effective.flatMap { series.nearest(to: $0) } ?? series.latest
        let v = point?.v ?? 0

        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 12) {
                Text("Capacity").font(.headline)
                Text("\(Int(round(v * 100)))%")
                    .font(.system(.title3, design: .rounded).monospacedDigit())
                mountPicker(vm: vm)
                if case .pinned = model.mode {
                    Label("Pinned", systemImage: "pin.fill")
                        .font(.caption)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Color.orange.opacity(0.2), in: Capsule())
                        .foregroundStyle(Color.orange)
                }
                Spacer()
                Text(snapshotAgeText(latest: latest))
                    .font(.caption).foregroundStyle(.secondary)
            }
            .padding(.horizontal, 16)

            MetricChart(
                samples: series.snapshot(),
                tint: {
                    if let w = warn, let c = critical {
                        return series.tint(warn: w, critical: c).color
                    }
                    return ThresholdTint.nominal.color
                }(),
                warn: warn,
                critical: critical,
                hoverAt: model.hoverAt,
                pinnedAt: pinned,
                yDomain: 0...1,
                yAxisLabel: { v in "\(Int(v * 100))%" },
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) }
            )
            // Shorter minHeight so a cramped window can still fit both
            // disk charts + the process table + toolbar. Flex up to infinity
            // when there's room; the two disk charts share equally since
            // both have layoutPriority(1).
            .frame(minHeight: 90, maxHeight: .infinity)
            .layoutPriority(1)
            .padding(.horizontal, 16)
        }
    }

    @ViewBuilder
    private func ioChart(vm: ServerViewModel) -> some View {
        let deviceSeries: DiskIOSeries.DeviceSeries = {
            switch model.diskDevice {
            case .total: return vm.diskIO.total
            case .device(let name): return vm.diskIO.series(forDevice: name) ?? vm.diskIO.total
            }
        }()
        let latest = deviceSeries.read.latest?.t ?? deviceSeries.write.latest?.t
        let pinned: Date? = {
            if case .pinned(let t) = model.mode { return t }
            return nil
        }()
        let effective = model.effectiveTimestamp(latest: latest)
        let rMBps: Double = {
            if let t = effective, let p = deviceSeries.read.nearest(to: t) { return p.v }
            return deviceSeries.read.latest?.v ?? 0
        }()
        let wMBps: Double = {
            if let t = effective, let p = deviceSeries.write.nearest(to: t) { return p.v }
            return deviceSeries.write.latest?.v ?? 0
        }()

        // The chart currently supports one series, so we plot read+write
        // as a single combined throughput line. The header shows them
        // individually so no information is lost.
        let combined = combinedIOSeries(read: deviceSeries.read, write: deviceSeries.write)

        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 12) {
                Text("I/O").font(.headline)
                let rTxt = Self.formatByteRate(bytesPerSec: rMBps * 1_048_576.0)
                let wTxt = Self.formatByteRate(bytesPerSec: wMBps * 1_048_576.0)
                Text("↓\(rTxt) · ↑\(wTxt)")
                    .font(.system(.title3, design: .rounded).monospacedDigit())
                devicePicker(vm: vm)
                Spacer()
            }
            .padding(.horizontal, 16)

            MetricChart(
                samples: combined,
                tint: ThresholdTint.nominal.color,
                warn: nil,
                critical: nil,
                hoverAt: model.hoverAt,
                pinnedAt: pinned,
                yDomain: nil,
                yAxisLabel: { v in Self.formatByteRate(bytesPerSec: v * 1_048_576.0) },
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) }
            )
            .frame(minHeight: 90, maxHeight: .infinity)
            .layoutPriority(1)
            .padding(.horizontal, 16)
        }
    }

    /// Merge read & write series into one "total throughput MB/s" series
    /// by summing values at matching timestamps. Both series are appended
    /// in lock-step by DiskIOSeries, so identical-indexed zipping is
    /// safe; fall back to zero-fill if one side is sparse.
    private func combinedIOSeries(read: MetricSeries, write: MetricSeries) -> [MetricPoint] {
        let r = read.snapshot()
        let w = write.snapshot()
        if r.count == w.count {
            return zip(r, w).map { MetricPoint(t: $0.0.t, v: $0.0.v + $0.1.v) }
        }
        // Unequal (shouldn't happen in practice): zip to the shorter,
        // trailing points appear zero-fill.
        let n = Swift.min(r.count, w.count)
        guard n > 0 else { return [] }
        var out: [MetricPoint] = []
        out.reserveCapacity(n)
        for i in 0..<n {
            out.append(MetricPoint(t: r[i].t, v: r[i].v + w[i].v))
        }
        return out
    }

    @ViewBuilder
    private func mountPicker(vm: ServerViewModel) -> some View {
        let mounts = vm.disksPerMount.mounts
        Menu {
            Button {
                model.diskMount = .max
            } label: {
                Label("Max (worst mount)", systemImage: model.diskMount == .max ? "checkmark" : "")
            }
            if !mounts.isEmpty { Divider() }
            ForEach(mounts, id: \.self) { m in
                Button {
                    model.diskMount = .mount(m)
                } label: {
                    Label(m, systemImage: model.diskMount == .mount(m) ? "checkmark" : "")
                }
            }
        } label: {
            switch model.diskMount {
            case .max: Text("Max").font(.caption)
            case .mount(let m): Text(m).font(.caption)
            }
        }
        .menuStyle(.borderlessButton)
        .frame(maxWidth: 200, alignment: .leading)
        .fixedSize()
    }

    @ViewBuilder
    private func devicePicker(vm: ServerViewModel) -> some View {
        let devices = vm.diskIO.devices
        Menu {
            Button {
                model.diskDevice = .total
            } label: {
                Label("Total (all devices)", systemImage: model.diskDevice == .total ? "checkmark" : "")
            }
            if !devices.isEmpty { Divider() }
            ForEach(devices, id: \.self) { d in
                Button {
                    model.diskDevice = .device(d)
                } label: {
                    Label(d, systemImage: model.diskDevice == .device(d) ? "checkmark" : "")
                }
            }
        } label: {
            switch model.diskDevice {
            case .total: Text("Total").font(.caption)
            case .device(let d): Text(d).font(.caption)
            }
        }
        .menuStyle(.borderlessButton)
        .frame(maxWidth: 200, alignment: .leading)
        .fixedSize()
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
                yDomain: model.metric == .net ? nil : 0...1,
                yAxisLabel: model.metric == .net
                    ? { v in Self.formatByteRate(bytesPerSec: v * 100.0 * 1_048_576.0) }
                    : { v in "\(Int(v * 100))%" },
                onHover: { t in
                    model.hoverAt = t
                },
                onPinTap: { t in
                    model.pin(at: t)
                }
            )
            // Flex vertically: the chart grows into whatever height is
            // left over after the fixed-height table, so resizing the
            // window expands the chart rather than the process list.
            .frame(minHeight: 240, maxHeight: .infinity)
            .layoutPriority(1)
            .padding(.horizontal, 16)
        }
    }

    @ViewBuilder
    private func processTable(vm: ServerViewModel) -> some View {
        let effective = model.effectiveTimestamp(latest: vm.procs.latest?.t)
        ProcessTable(
            series: vm.procs,
            effectiveAt: effective,
            available: vm.procsAvailable,
            isRootSampler: vm.procsRoot,
            metric: model.metric
        )
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
            let rxMBps: Double = {
                if let t = effective, let p = vm.netRx.nearest(to: t) { return p.v }
                return vm.netRx.latest?.v ?? vm.netRxMBps
            }()
            let txMBps: Double = {
                if let t = effective, let p = vm.netTx.nearest(to: t) { return p.v }
                return vm.netTx.latest?.v ?? vm.netTxMBps
            }()
            let rx = Self.formatByteRate(bytesPerSec: rxMBps * 1_048_576.0)
            let tx = Self.formatByteRate(bytesPerSec: txMBps * 1_048_576.0)
            text = "↓\(rx) · ↑\(tx)"
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

    /// Formats a byte-rate with an adaptive unit so small values stay readable.
    /// 500 B/s stays in bytes; 120_000 B/s becomes "117.2 KB/s"; >1 MiB/s in MB/s.
    static func formatByteRate(bytesPerSec: Double) -> String {
        let v = max(0, bytesPerSec)
        if v < 1_024 {
            return String(format: "%.0f B/s", v)
        }
        if v < 1_048_576 {
            return String(format: "%.1f KB/s", v / 1_024)
        }
        if v < 1_073_741_824 {
            return String(format: "%.1f MB/s", v / 1_048_576)
        }
        return String(format: "%.2f GB/s", v / 1_073_741_824)
    }
}
