import SwiftUI

/// The rail on the left. On the right the chart of the selected metric
/// and the top processes at the cursor.
struct OverviewTab: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let frame: WorkbenchFrame
    let score: HealthScore
    var node: Node? = nil
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil

    @State private var snapshot = LocalSnapshot.empty

    private struct SnapKey: Hashable {
        let at: Date?
        let sort: ProcSort
        let latest: Date?
        let hovering: Bool
    }

    var body: some View {
        let latestProc = vm.procs.latest?.t
        let key = SnapKey(
            at: model.effectiveTimestamp(latest: latestProc), sort: model.procSort,
            latest: latestProc, hovering: model.hoverAt != nil
        )
        HStack(alignment: .top, spacing: 12) {
            MetricRail(
                vm: vm, frame: frame, score: score, window: frame.window,
                cursor: model.hoverAt ?? pinned, selection: $model.rail
            )
            GeometryReader { geo in
                VStack(spacing: 12) {
                    chartCard.frame(height: max(190, geo.size.height * 0.42))
                    TopProcessPanel(
                        rows: snapshot.rows, bucketLabel: snapshot.label(now: Date()),
                        state: CursorState(model: model), loading: false,
                        sort: Binding(get: { model.procSort }, set: { model.procSortOverride = $0 }),
                        onOpen: { model.openProcess = $0 },
                        placeholder: placeholder, total: snapshot.total, node: node,
                        longProcs: vm.longProcs, onIgnore: onIgnore,
                        compact: model.openProcess != nil
                    )
                }
            }
        }
        .padding(.horizontal, 14).padding(.bottom, 12)
        .task(id: key) {
            // A moving cursor builds only its last position, at about 10 fps.
            if key.hovering {
                try? await Task.sleep(for: .milliseconds(100))
                guard !Task.isCancelled else { return }
            }
            snapshot = LocalSnapshot(series: vm.procs, at: key.at, sort: key.sort)
        }
    }

    private var pinned: Date? {
        if case .pinned(let t) = model.mode { return t }
        return nil
    }

    private var placeholder: String? {
        if !vm.procsAvailable { return "Per-process collection is off on this host." }
        if let n = vm.procsSkippedTotal {
            return "Process scan skipped: \(HealthStatus.count(n)) processes (over the sampler's scan limit)."
        }
        return snapshot.rows.isEmpty ? "Waiting for the first process sample…" : nil
    }

    // MARK: Chart

    private var chartCard: some View {
        let m = model.rail
        let series = frame.series[m] ?? []
        let lim = m.limits(vm.thresholds)
        return VStack(alignment: .leading, spacing: 6) {
            chartHead(m, series: series, lim: lim)
            MetricChart(
                series: series, xDomain: frame.window,
                warn: lim.warn, critical: lim.critical,
                hoverAt: model.hoverAt, pinnedAt: pinned,
                yDomain: m.yDomain, yAxisLabel: m.axisLabel, valueFormat: m.format,
                selectionRange: model.pendingSelection,
                onHover: { model.hoverAt = $0 },
                onPinTap: { model.pin(at: $0) },
                onDragBegin: { model.beginSelection(at: $0) },
                onDragUpdate: { model.updateSelection(to: $0) },
                onDragEnd: { model.endSelection() }
            )
            .frame(maxHeight: .infinity)
            HStack {
                Text("hover to inspect · click to pin · drag to zoom")
                Spacer()
            }
            .font(.system(size: 11, design: .monospaced))
            .foregroundStyle(WB.faint)
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .wbCard()
    }

    private func chartHead(_ m: RailMetric, series: [ChartSeries], lim: (warn: Double?, critical: Double?)) -> some View {
        let values = (series.first?.points ?? []).map(\.v)
        let avg = values.isEmpty ? nil : values.reduce(0, +) / Double(values.count)
        var stats: [String] = []
        if let avg, let peak = values.max() {
            stats.append("avg " + m.format(avg))
            stats.append("peak " + m.format(peak))
        }
        if let w = lim.warn { stats.append("warn " + m.format(w)) }
        if let c = lim.critical { stats.append("critical " + m.format(c)) }
        let cursor = model.hoverAt ?? pinned
        return HStack(alignment: .firstTextBaseline, spacing: 10) {
            KLabel(m.displayName)
            Text(stats.joined(separator: " · "))
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(WB.faint)
                .lineLimit(1)
            Spacer(minLength: 8)
            bigValue(m, at: cursor)
            Text(model.hoverAt != nil ? "at " + model.hoverAt!.formatted(date: .omitted, time: .standard)
                 : pinned != nil ? "pinned" : "now")
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(WB.faint)
        }
    }

    @ViewBuilder
    private func bigValue(_ m: RailMetric, at t: Date?) -> some View {
        if m.hasTwoSeries {
            HStack(spacing: 8) {
                ForEach(Array((frame.series[m] ?? []).enumerated()), id: \.offset) { i, s in
                    Text(frame.value(m, i, at: t).map(m.format) ?? "—").foregroundStyle(s.color)
                }
            }
            .font(.system(size: 20, weight: .semibold).monospacedDigit())
        } else {
            let v = frame.value(m, at: t)
            Text(v.map(m.format) ?? "—")
                .font(.system(size: 22, weight: .semibold).monospacedDigit())
                .foregroundStyle(v.map { m.tint(of: $0, thresholds: vm.thresholds).valueColor } ?? WB.faint)
        }
    }
}
