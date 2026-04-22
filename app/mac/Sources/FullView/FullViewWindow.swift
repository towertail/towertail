import SwiftUI

struct FullViewWindow: View {
    let context: FullViewContext
    @Environment(ServerStore.self) private var store
    @Environment(AppSettings.self) private var settings
    @Environment(NodeStore.self) private var nodeStore
    @State private var model: FullViewModel
    /// Active host id. Seeded from `context` but mutable so the header's
    /// host picker can swap the view between servers without re-opening
    /// the window.
    @State private var activeHostId: UUID
    /// Throttle rate for chart + process-table rebuilds. Ingests can
    /// arrive at 0.5s (local) or faster; rebuilding SwiftUI Charts +
    /// Table on every ingest is wasted work. 5Hz is smooth visually and
    /// matches the table's own throttle so both panels update in lockstep.
    private static let renderInterval: TimeInterval = 0.2

    /// Cached snapshots for the active chart. Refreshed on a 5Hz timer so
    /// SwiftUI Charts only diffs when we say so, not on every ingest. The
    /// body reads these arrays; hover/pin changes rebuild the body without
    /// touching the underlying `MetricSeries`.
    @State private var cachedMainSamples: [MetricPoint] = []
    @State private var cachedDiskCapacity: [MetricPoint] = []
    @State private var cachedDiskIO: [MetricPoint] = []
    /// Timer publisher driving the cache refresh. `.autoconnect` subscribes
    /// the moment the view mounts and releases on teardown.
    private let renderTimer = Timer.publish(every: FullViewWindow.renderInterval, tolerance: 0.05, on: .main, in: .common).autoconnect()

    init(context: FullViewContext) {
        self.context = context
        _model = State(initialValue: FullViewModel(metric: context.metric))
        _activeHostId = State(initialValue: context.hostId)
    }

    var body: some View {
        let vm = store.serverVMs.first(where: { $0.id == activeHostId })
        VStack(spacing: 0) {
            header(vm: vm)
            Divider()
            content(vm: vm)
        }
        // Baseline layout with the DISK tab as the tallest worst case:
        // header+transport (~40) + picker (~36) + capacity chart (min 90) +
        // i/o chart (min 90) + process table (min 160) + paddings/dividers
        // ≈ 500. Keep a little breathing room above that minimum so the
        // window doesn't feel squished on first open.
        .frame(minWidth: 860, minHeight: 520)
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.space) {
            guard let vm else { return .ignored }
            let latest = currentSeries(vm: vm).latest?.t
            model.togglePlayPause(latest: latest)
            return .handled
        }
        .onAppear {
            ActivationPolicyCoordinator.shared.acquire()
            // Only the full view reads proc history, so we defer the
            // SQLite hydration until someone actually opens this window.
            // Launch stays cheap even with tens of MB of proc snapshots
            // on disk; we pay the decode once, here, per session.
            store.ensureProcsHydrated(for: activeHostId)
            refreshCaches(vm: vm)
        }
        .onDisappear { ActivationPolicyCoordinator.shared.release() }
        .onReceive(renderTimer) { _ in
            refreshCaches(vm: vm)
        }
        .onChange(of: model.metric) { _, _ in refreshCaches(vm: vm) }
        .onChange(of: model.zoomRange) { _, _ in refreshCaches(vm: vm) }
        .onChange(of: model.diskMount) { _, _ in refreshCaches(vm: vm) }
        .onChange(of: model.diskDevice) { _, _ in refreshCaches(vm: vm) }
        .onChange(of: activeHostId) { _, newId in
            // Switching hosts: drop stale caches and hydrate the new one's
            // proc history (same lazy path as onAppear). The cached chart
            // arrays belong to the old host so clearing them avoids a
            // 1-frame flash of the previous server's data.
            cachedMainSamples = []
            cachedDiskCapacity = []
            cachedDiskIO = []
            // Per-mount / per-device selections rarely transfer between
            // hosts (different mountpoints, different device names), so
            // reset them to the sensible default.
            model.diskMount = .max
            model.diskDevice = .total
            store.ensureProcsHydrated(for: newId)
            refreshCaches(vm: store.serverVMs.first(where: { $0.id == newId }))
        }
    }

    /// Pull snapshots from the live series and publish them to the cached
    /// @State arrays. Everything below the body reads from the caches —
    /// this is the only place we call `series.snapshot()`.
    private func refreshCaches(vm: ServerViewModel?) {
        guard let vm else { return }
        switch model.metric {
        case .cpu:  updateMain(clipToZoom(vm.cpu.snapshot()))
        case .mem:  updateMain(clipToZoom(vm.mem.snapshot()))
        case .net:  updateMain(clipToZoom(vm.net.snapshot()))
        case .disk:
            let capacity: [MetricPoint] = {
                switch model.diskMount {
                case .max: return vm.disksPerMount.max.snapshot()
                case .mount(let m): return (vm.disksPerMount.series(forMount: m) ?? vm.disksPerMount.max).snapshot()
                }
            }()
            let deviceSeries: DiskIOSeries.DeviceSeries = {
                switch model.diskDevice {
                case .total: return vm.diskIO.total
                case .device(let name): return vm.diskIO.series(forDevice: name) ?? vm.diskIO.total
                }
            }()
            let combined = combinedIOSeries(read: deviceSeries.read, write: deviceSeries.write)
            let clippedCapacity = clipToZoom(capacity)
            let clippedIO = clipToZoom(combined)
            if cachedDiskCapacity != clippedCapacity { cachedDiskCapacity = clippedCapacity }
            if cachedDiskIO != clippedIO { cachedDiskIO = clippedIO }
        }
    }

    private func updateMain(_ samples: [MetricPoint]) {
        if cachedMainSamples != samples { cachedMainSamples = samples }
    }

    @ViewBuilder
    private func header(vm: ServerViewModel?) -> some View {
        HStack(spacing: 10) {
            hostPicker(current: vm)
            Text(vm?.dnsName ?? "")
                .font(.callout)
                .foregroundStyle(.secondary)
            Spacer()
            transportControls(vm: vm)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
    }

    /// Dropdown that swaps the window's active host in place. The label
    /// mimics the previous bold hostname so the window still reads as
    /// "I am looking at {host}" at a glance — the chevron hint signals
    /// that it's clickable.
    @ViewBuilder
    private func hostPicker(current: ServerViewModel?) -> some View {
        Menu {
            ForEach(store.serverVMs) { other in
                Button {
                    if other.id != activeHostId {
                        activeHostId = other.id
                    }
                } label: {
                    Label(
                        other.hostname,
                        systemImage: other.id == activeHostId ? "checkmark" : ""
                    )
                }
            }
        } label: {
            HStack(spacing: 4) {
                Text(current?.hostname ?? "Unknown host")
                    .font(.title3).bold()
                Image(systemName: "chevron.down")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .help("Switch server")
    }

    @ViewBuilder
    private func transportControls(vm: ServerViewModel?) -> some View {
        HStack(spacing: 6) {
            if case .pinned = model.mode {
                Button("Unpin") { model.unpin() }
            }
            if model.canZoomIn {
                Button {
                    model.zoomInToSelection()
                } label: {
                    Label("Zoom in", systemImage: "plus.magnifyingglass")
                }
                .help("Zoom the chart to the highlighted range")
            }
            if model.canResetZoom {
                Button {
                    model.resetZoom()
                } label: {
                    Label("Reset zoom", systemImage: "arrow.up.left.and.arrow.down.right")
                }
                .help("Return to the full two-hour window")
            }
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
        }
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
                samples: cachedDiskCapacity,
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
                selectionRange: model.pendingSelection,
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { }
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
                samples: cachedDiskIO,
                tint: ThresholdTint.nominal.color,
                warn: nil,
                critical: nil,
                hoverAt: model.hoverAt,
                pinnedAt: pinned,
                yDomain: nil,
                yAxisLabel: { v in Self.formatByteRate(bytesPerSec: v * 1_048_576.0) },
                selectionRange: model.pendingSelection,
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { }
            )
            .frame(minHeight: 90, maxHeight: .infinity)
            .layoutPriority(1)
            .padding(.horizontal, 16)
        }
    }

    private func clipToZoom(_ samples: [MetricPoint]) -> [MetricPoint] {
        guard let z = model.zoomRange else { return samples }
        return samples.filter { $0.t >= z.lowerBound && $0.t <= z.upperBound }
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
                samples: cachedMainSamples,
                tint: tint(vm: vm, series: series, metric: model.metric),
                warn: warn,
                critical: critical,
                hoverAt: model.hoverAt,
                pinnedAt: pinned,
                yDomain: model.metric == .net ? nil : 0...1,
                yAxisLabel: model.metric == .net
                    ? { v in Self.formatByteRate(bytesPerSec: v * 100.0 * 1_048_576.0) }
                    : { v in "\(Int(v * 100))%" },
                selectionRange: model.pendingSelection,
                onHover: { t in
                    model.hoverAt = t
                },
                onPinTap: { t in
                    model.pin(at: t)
                },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { }
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
            metric: model.metric,
            node: nodeStore.node(withId: activeHostId)
        )
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
