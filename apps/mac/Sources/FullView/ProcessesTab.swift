import SwiftUI

/// The offenders of the window, or the listening ports.
struct ProcessesTab: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let frame: WorkbenchFrame
    var node: Node? = nil
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil
    /// Set for a Cloud host: the offenders come from the server.
    var loader: (any CloudHistoryLoader)? = nil

    @State private var offenders: [Offender] = []
    @State private var loading = false
    @State private var loadError: String?

    private struct RankKey: Hashable {
        let window: ClosedRange<Date>
        let sort: ProcSort
        let latest: Date?
    }

    var body: some View {
        // The key moves with the window, so a new ranking runs about every 10 s.
        let key = RankKey(
            window: Self.rounded(frame.window, grid: loader == nil ? 10 : 60), sort: model.procSort,
            latest: loader == nil ? vm.procs.latest.map { Date(timeIntervalSince1970: ($0.t.timeIntervalSince1970 / 10).rounded(.down) * 10) } : nil
        )
        VStack(spacing: 12) {
            HStack {
                WBSegments(items: ProcessesView.allCases, selection: $model.processesView, label: \.displayName)
                Spacer()
                if model.processesView == .offenders {
                    WBSegments(items: ProcSort.allCases, selection: Binding(
                        get: { model.procSort }, set: { model.procSortOverride = $0 }
                    ), label: \.displayName)
                }
            }
            switch model.processesView {
            case .offenders:
                chartCard.frame(height: 210)
                table
            case .ports:
                PortsTable(
                    ports: vm.ports, available: vm.portsAvailable,
                    isRootSampler: vm.ports?.root ?? vm.procsRoot, node: node
                )
                .frame(maxHeight: .infinity, alignment: .top)
            }
        }
        .padding(.horizontal, 14).padding(.bottom, 12)
        .task(id: key) { await rank(key) }
    }

    private static func rounded(_ w: ClosedRange<Date>, grid: TimeInterval) -> ClosedRange<Date> {
        func r(_ d: Date) -> Date { Date(timeIntervalSince1970: (d.timeIntervalSince1970 / grid).rounded(.down) * grid) }
        return r(w.lowerBound)...r(w.upperBound)
    }

    private func rank(_ key: RankKey) async {
        if let loader {
            loading = true
            loadError = nil
            do {
                let r = try await loader.offenders(host: vm.id, window: key.window, by: key.sort)
                guard !Task.isCancelled else { return }
                offenders = r.processes.map { Offender($0, in: r) }
            } catch {
                guard !Task.isCancelled else { return }
                offenders = []
                loadError = CloudHistoryError.message(error)
            }
            loading = false
            return
        }
        let snaps = vm.procs.snapshots(in: key.window)
        let result = await Task.detached(priority: .userInitiated) {
            OffenderRanking(snapshots: snaps, window: key.window, by: key.sort).top(100)
        }.value
        guard !Task.isCancelled else { return }
        offenders = result
    }

    private var pinned: Date? {
        if case .pinned(let t) = model.mode { return t }
        return nil
    }

    // MARK: Chart

    private var selected: Offender? {
        model.offenderSelected.flatMap { ref in offenders.first { $0.ref.key == ref.key } }
    }

    private var chartCard: some View {
        let host = frame.series[.cpu] ?? []
        var series = host
        let cores = Double(max(vm.cpuCores ?? 1, 1))
        if let sel = selected {
            let pts = sel.series.enumerated().map { i, v in
                MetricPoint(t: sel.time(of: i, in: frame.window, count: sel.series.count), v: v / (100 * cores))
            }
            .filter { frame.window.contains($0.t) }
            series.append(ChartSeries(name: sel.ref.name, color: WB.write, points: pts))
        }
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                KLabel("Host and selected process")
                Text(selected == nil ? "Select a row to draw a process next to the host CPU."
                     : "share of host CPU")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(WB.faint)
                Spacer()
            }
            MetricChart(
                series: series, xDomain: frame.window,
                warn: nil, critical: nil,
                hoverAt: model.hoverAt, pinnedAt: pinned,
                yDomain: 0...1,
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

    // MARK: Table

    private var ranks: [String: Int] {
        Dictionary(uniqueKeysWithValues: offenders.enumerated().map { ($1.id, $0 + 1) })
    }

    private var table: some View {
        let ranks = ranks
        let maxAvg = max(offenders.map(\.avg).max() ?? 1, 1)
        return Group {
            if offenders.isEmpty {
                Text(loadError ?? (loading ? "Loading the offenders…" : vm.procsAvailable || loader != nil ? "No process data in this window." : "Per-process collection is off on this host."))
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(offenders, selection: Binding(
                    get: { selected?.id },
                    set: { key in
                        let ref = offenders.first { $0.id == key }?.ref
                        model.offenderSelected = ref
                        if let ref { model.openProcess = ref }
                    }
                )) {
                    TableColumn("#") { o in Text("\(ranks[o.id] ?? 0)").foregroundStyle(.secondary).monospacedDigit() }
                        .width(28)
                    TableColumn("Process") { o in
                        HStack(spacing: 6) {
                            Text(o.ref.name).fontWeight(.semibold).lineLimit(1)
                            if !o.ref.cmd.isEmpty, o.ref.cmd != o.ref.name {
                                Text(o.ref.cmd).font(.system(size: 11, design: .monospaced))
                                    .foregroundStyle(WB.faint).lineLimit(1)
                            }
                        }
                    }
                    .width(min: 120)
                    TableColumn("Avg") { o in
                        HStack(spacing: 6) {
                            RoundedRectangle(cornerRadius: 2).fill(WB.cpu.opacity(0.55))
                                .frame(width: CGFloat(o.avg / maxAvg) * 36, height: 5)
                            Text(String(format: "%.1f%%", o.avg)).monospacedDigit()
                        }
                    }
                    .width(min: 80, ideal: 96, max: 110)
                    TableColumn("Peak") { o in
                        Text(String(format: "%.1f%%", o.peak)).monospacedDigit().foregroundStyle(.secondary)
                    }
                    .width(min: 56, ideal: 68, max: 80)
                    TableColumn("Memory peak") { o in Text(ProcessTable.formatBytes(o.rssMax)).monospacedDigit() }
                        .width(min: 76, ideal: 90, max: 100)
                    TableColumn("Time ≥ 50% CPU") { o in
                        Text(o.highFor > 0 ? ShortDuration.long(o.highFor) : "—").monospacedDigit().foregroundStyle(.secondary)
                    }
                    .width(min: 76, ideal: 100, max: 120)
                    TableColumn("Over the window") { o in sparkline(o) }
                        .width(min: 70, ideal: 100, max: 140)
                    TableColumn("") { o in rowMenu(o) }
                        .width(34)
                }
            }
        }
        .wbCard(fill: WB.sidebar)
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private func sparkline(_ o: Offender) -> some View {
        let w = frame.window
        let span = w.upperBound.timeIntervalSince(w.lowerBound)
        let pts = o.series.enumerated().map { i, v in
            MetricPoint(t: o.time(of: i, in: w, count: o.series.count), v: v)
        }
        .filter { w.contains($0.t) }
        return CanvasSparkline(
            samples: pts, tint: WB.cpu, warn: nil, windowSeconds: span,
            yDomain: nil, height: 20, xDomain: w
        )
    }

    private func rowMenu(_ o: Offender) -> some View {
        Menu {
            Button("Open process") { model.offenderSelected = o.ref; model.openProcess = o.ref }
            if !o.ref.cmd.isEmpty { Button("Copy command") { Pasteboard.copy(o.ref.cmd) } }
            if let onIgnore {
                Divider()
                Button("Ignore \"\(o.ref.name)\" on all hosts") { onIgnore(o.ref.name, .allHosts) }
                if let node {
                    Button("Ignore \"\(o.ref.name)\" on this host") { onIgnore(o.ref.name, .host(node.id)) }
                }
            }
        } label: {
            Image(systemName: "ellipsis")
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
    }
}
