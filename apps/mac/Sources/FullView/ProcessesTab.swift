import SwiftUI

/// The offenders of the window, or the listening ports.
struct ProcessesTab: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let frame: WorkbenchFrame
    var node: Node? = nil
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil

    @State private var offenders: [Offender] = []

    private struct RankKey: Hashable {
        let window: ClosedRange<Date>
        let sort: ProcSort
        let latest: Date?
    }

    var body: some View {
        // The key moves with the window, so a new ranking runs about every 10 s.
        let key = RankKey(
            window: Self.rounded(frame.window), sort: model.procSort,
            latest: vm.procs.latest.map { Date(timeIntervalSince1970: ($0.t.timeIntervalSince1970 / 10).rounded(.down) * 10) }
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

    private static func rounded(_ w: ClosedRange<Date>) -> ClosedRange<Date> {
        func r(_ d: Date) -> Date { Date(timeIntervalSince1970: (d.timeIntervalSince1970 / 10).rounded(.down) * 10) }
        return r(w.lowerBound)...r(w.upperBound)
    }

    private func rank(_ key: RankKey) async {
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
            let span = frame.window.upperBound.timeIntervalSince(frame.window.lowerBound)
            let step = span / Double(max(sel.series.count, 1))
            let pts = sel.series.enumerated().map { i, v in
                MetricPoint(t: frame.window.lowerBound.addingTimeInterval(Double(i) * step + step / 2), v: v / (100 * cores))
            }
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
                Text(vm.procsAvailable ? "No process data in this window." : "Per-process collection is off on this host.")
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(offenders, selection: Binding(
                    get: { model.offenderSelected?.key },
                    set: { key in model.offenderSelected = offenders.first { $0.id == key }?.ref }
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
                    .width(min: 160, ideal: 280)
                    TableColumn("Avg") { o in
                        HStack(spacing: 6) {
                            RoundedRectangle(cornerRadius: 2).fill(WB.cpu.opacity(0.55))
                                .frame(width: CGFloat(o.avg / maxAvg) * 36, height: 5)
                            Text(String(format: "%.1f%%", o.avg)).monospacedDigit()
                        }
                    }
                    .width(min: 80, ideal: 100)
                    TableColumn("Peak") { o in
                        Text(String(format: "%.1f%%", o.peak)).monospacedDigit().foregroundStyle(.secondary)
                    }
                    .width(min: 60, ideal: 70)
                    TableColumn("Memory peak") { o in Text(ProcessTable.formatBytes(o.rssMax)).monospacedDigit() }
                        .width(min: 80, ideal: 100)
                    TableColumn("Time ≥ 50% CPU") { o in
                        Text(o.highFor > 0 ? ShortDuration.long(o.highFor) : "—").monospacedDigit().foregroundStyle(.secondary)
                    }
                    .width(min: 90, ideal: 120)
                    TableColumn("Over the window") { o in sparkline(o) }
                        .width(min: 90, ideal: 130)
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
        let step = span / Double(max(o.series.count, 1))
        let pts = o.series.enumerated().map { i, v in
            MetricPoint(t: w.lowerBound.addingTimeInterval(Double(i) * step), v: v)
        }
        return CanvasSparkline(
            samples: pts, tint: WB.cpu, warn: nil, windowSeconds: span,
            yDomain: nil, height: 20, xDomain: w
        )
    }

    private func rowMenu(_ o: Offender) -> some View {
        Menu {
            Button("Show over time") { model.offenderSelected = o.ref }
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
