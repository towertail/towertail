import SwiftUI

/// Mounts with capacity and growth, the disk I/O, and the folder scan.
struct DisksTab: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let frame: WorkbenchFrame
    let node: Node?
    /// The REST series of a Cloud host. Nil for local and SSH hosts.
    var cloud: WorkbenchSeries? = nil
    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend

    var body: some View {
        if model.diskSubTab == .usage, let backend, let node {
            let m = model.usageModel(for: node, backend: backend, defaultScope: vm.hostOS == "linux" ? "/" : "~")
            DiskUsageView(
                model: m, vm: vm, via: scanVia(node: node, backend: backend),
                scopeChoices: ["~"] + vm.disksPerMount.mounts.sorted(),
                allowScan: !(backend is RemoteBackend) || node.allowDiskScan,
                onAllowScans: backend is RemoteBackend ? {
                    guard var n = nodeStore.node(withId: node.id) else { return }
                    n.allowDiskScan = true
                    try await backend.updateNode(n)
                } : nil
            ) {
                subTabPicker
            }
            .padding(.horizontal, 14).padding(.bottom, 12)
        } else {
            VStack(spacing: 12) {
                HStack {
                    subTabPicker
                    Spacer()
                }
                mounts
                HStack(spacing: 12) {
                    capacityCard
                    ioCard
                }
                .frame(minHeight: 180, maxHeight: .infinity)
            }
            .padding(.horizontal, 14).padding(.bottom, 12)
        }
    }

    private var subTabPicker: some View {
        WBSegments(items: DiskSubTab.allCases, selection: $model.diskSubTab, label: \.displayName)
    }

    private func scanVia(node: Node, backend: any Backend) -> ScanVia {
        if backend is RemoteBackend { return .server }
        switch node.kind {
        case .local: return .local
        case .ssh: return .ssh(user: node.sshUser ?? "")
        case .cloud: return .server
        }
    }

    private var pinned: Date? {
        if case .pinned(let t) = model.mode { return t }
        return nil
    }

    /// The points of one mount: the REST points of a Cloud host, else the live series.
    private func mountPoints(_ mount: String) -> [MetricPoint] {
        if let cloud { return cloud.mountPoints(mount) }
        return vm.disksPerMount.series(forMount: mount)?.snapshot() ?? []
    }

    private var mountNames: [String] {
        cloud == nil ? vm.disksPerMount.mounts : vm.latestDisks.map(\.mount).sorted()
    }

    // MARK: Mounts

    private var mounts: some View {
        let disks = vm.latestDisks.sorted { $0.mount < $1.mount }
        return VStack(alignment: .leading, spacing: 0) {
            KLabel("Mounts").padding(.horizontal, 14).padding(.vertical, 10)
            if disks.isEmpty {
                Text("No disk data from this host's sampler.")
                    .font(.callout).foregroundStyle(.secondary)
                    .padding(.horizontal, 14).padding(.bottom, 12)
            }
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(disks, id: \.mount) { d in
                        Divider().overlay(WB.border)
                        mountRow(d)
                    }
                }
            }
            .frame(height: min(190, CGFloat(disks.count) * 40))
        }
        .wbCard()
    }

    private func mountRow(_ d: DiskSample) -> some View {
        let frac = d.total > 0 ? Double(d.used) / Double(d.total) : 0
        let tint = RailMetric.disk.tint(of: frac, thresholds: vm.thresholds)
        let selected = model.diskMount == .mount(d.mount)
        let points = mountPoints(d.mount)
        return HStack(spacing: 10) {
            Text(d.mount).font(.system(size: 13, weight: .medium)).lineLimit(1).truncationMode(.middle)
                .frame(width: 180, alignment: .leading)
            Text(d.fs).font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                .frame(width: 60, alignment: .leading)
            Text("\(Int((frac * 100).rounded()))%").font(.system(size: 13, weight: .semibold).monospacedDigit())
                .foregroundStyle(tint.valueColor).frame(width: 44, alignment: .trailing)
            Text("\(ProcessTable.formatBytes(d.used)) / \(ProcessTable.formatBytes(d.total))")
                .font(.system(size: 12).monospacedDigit()).foregroundStyle(WB.muted)
                .frame(width: 140, alignment: .leading)
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(WB.border)
                    Capsule().fill(tint.color).frame(width: geo.size.width * min(max(frac, 0.01), 1))
                }
            }
            .frame(width: 110, height: 5)
            Text(inodeText(d)).font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                .frame(width: 100, alignment: .leading)
            Text(growth(points, now: frame.window.upperBound) ?? "")
                .font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.pin)
                .lineLimit(1)
            Spacer(minLength: 8)
            if points.count > 1 {
                CanvasSparkline(
                    samples: points.filter { frame.window.contains($0.t) },
                    tint: tint.color, warn: nil,
                    windowSeconds: frame.window.upperBound.timeIntervalSince(frame.window.lowerBound),
                    yDomain: 0...1, height: 22, xDomain: frame.window
                )
                .frame(width: 110, height: 22)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 6)
        .background(selected ? WB.hover : .clear)
        .contentShape(Rectangle())
        .onTapGesture { model.diskMount = selected ? .max : .mount(d.mount) }
    }

    private func inodeText(_ d: DiskSample) -> String {
        guard let f = HealthStatus.fraction(d.inodesUsed, d.inodesTotal) ?? cloud?.inodeRatio(d.mount) else { return "" }
        return "inodes \(Int((f * 100).rounded()))%"
    }

    /// Least-squares slope of the series. Returns text when the disk
    /// fills in less than a year.
    private func growth(_ pts: [MetricPoint], now: Date) -> String? {
        guard pts.count >= 20,
              let first = pts.first, let last = pts.last,
              last.t.timeIntervalSince(first.t) >= 600 else { return nil }
        let xs = pts.map { $0.t.timeIntervalSince(first.t) }
        let n = Double(pts.count)
        let mx = xs.reduce(0, +) / n
        let my = pts.map(\.v).reduce(0, +) / n
        let den = xs.reduce(0) { $0 + ($1 - mx) * ($1 - mx) }
        guard den > 0 else { return nil }
        let slope = zip(xs, pts).reduce(0) { $0 + ($1.0 - mx) * ($1.1.v - my) } / den
        guard slope > 1e-9 else { return nil }
        let left = (1 - last.v) / slope
        guard left > 0, left < 365 * 86_400 else { return nil }
        if left < 2 * 86_400 { return "full in about \(max(1, Int((left / 3_600).rounded()))) h" }
        return "full in about \(Int((left / 86_400).rounded())) d"
    }

    // MARK: Charts

    private var capacityCard: some View {
        let points: [MetricPoint]
        switch model.diskMount {
        case .max: points = frame.points(.disk)
        case .mount(let m):
            let own = mountPoints(m)
            points = (own.isEmpty ? frame.points(.disk) : own).filter { frame.window.contains($0.t) }
        }
        let lim = RailMetric.disk.limits(vm.thresholds)
        let v = cursorValue(points)
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                KLabel("Capacity")
                mountPicker
                Spacer()
                Text(v.map(RailMetric.disk.format) ?? "—")
                    .font(.system(size: 20, weight: .semibold).monospacedDigit())
                    .foregroundStyle(v.map { RailMetric.disk.tint(of: $0, thresholds: vm.thresholds).valueColor } ?? WB.faint)
            }
            MetricChart(
                series: [ChartSeries(name: "Disk", color: WB.disk, points: points)],
                xDomain: frame.window, warn: lim.warn, critical: lim.critical,
                hoverAt: model.hoverAt, pinnedAt: pinned, yDomain: 0...1,
                yAxisLabel: RailMetric.disk.axisLabel, valueFormat: RailMetric.disk.format,
                selectionRange: model.pendingSelection,
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { model.endSelection() }
            )
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .wbCard()
    }

    private var ioCard: some View {
        let read: [MetricPoint], write: [MetricPoint]
        if cloud != nil {
            (read, write) = (frame.points(.io, 0), frame.points(.io, 1))
        } else {
            let dev: DiskIOSeries.DeviceSeries = {
                switch model.diskDevice {
                case .total: return vm.diskIO.total
                case .device(let n): return vm.diskIO.series(forDevice: n) ?? vm.diskIO.total
                }
            }()
            func clip(_ s: MetricSeries) -> [MetricPoint] { s.snapshot().filter { frame.window.contains($0.t) } }
            (read, write) = (clip(dev.read), clip(dev.write))
        }
        let m = RailMetric.io
        let r = cursorValue(read), w = cursorValue(write)
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                KLabel("Disk I/O")
                if vm.liveSource != .cloud { devicePicker }
                Spacer()
                HStack(spacing: 8) {
                    Text("R " + (r.map(m.format) ?? "—")).foregroundStyle(WB.read)
                    Text("W " + (w.map(m.format) ?? "—")).foregroundStyle(WB.write)
                }
                .font(.system(size: 15, weight: .semibold).monospacedDigit())
            }
            MetricChart(
                series: [
                    ChartSeries(name: "Read", color: WB.read, points: read),
                    ChartSeries(name: "Write", color: WB.write, points: write),
                ],
                xDomain: frame.window, warn: nil, critical: nil,
                hoverAt: model.hoverAt, pinnedAt: pinned, yDomain: nil,
                yAxisLabel: m.axisLabel, valueFormat: m.format,
                selectionRange: model.pendingSelection,
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { model.endSelection() }
            )
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .wbCard()
    }

    private func cursorValue(_ pts: [MetricPoint]) -> Double? {
        guard let t = model.hoverAt ?? pinned else { return pts.last?.v }
        return MetricChart.nearest(in: pts, to: t)?.v
    }

    private var mountPicker: some View {
        Menu {
            Button {
                model.diskMount = .max
            } label: {
                Label("Max (worst mount)", systemImage: model.diskMount == .max ? "checkmark" : "")
            }
            let all = mountNames
            if !all.isEmpty { Divider() }
            ForEach(all, id: \.self) { m in
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
        .fixedSize()
    }

    private var devicePicker: some View {
        Menu {
            Button {
                model.diskDevice = .total
            } label: {
                Label("Total (all devices)", systemImage: model.diskDevice == .total ? "checkmark" : "")
            }
            let all = vm.diskIO.devices
            if !all.isEmpty { Divider() }
            ForEach(all, id: \.self) { d in
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
        .fixedSize()
    }
}
