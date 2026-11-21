import SwiftUI

/// What the hover card of one metric shows. Built from the view model, so a test needs no view.
struct MetricHoverContent: Equatable {
    static let window: TimeInterval = 15 * 60
    static let topCount = 3
    static let staleProcs: TimeInterval = 120

    static func supports(_ metric: Metric) -> Bool {
        metric == .cpu || metric == .mem || metric == .disk
    }

    enum Chart: Equatable {
        case percent([MetricPoint], warn: Double)
        /// Total disk read and write in MB/s.
        case readWrite(read: [MetricPoint], write: [MetricPoint])
    }

    enum Procs: Equatable {
        /// `others`: the rest of the metric, "175%" or "3.2 GB".
        case top([Line], others: String?)
        case note(String)
    }

    struct Line: Equatable {
        /// The row below the title: path, what, parent.
        struct Second: Equatable {
            let path: ProcCulprit.PathParts?
            let what: String?
            let parent: String?
        }

        /// The full text, "node ×4 · sling · vitest run".
        let label: String
        /// "node ×4"
        let title: String
        let second: Second?
        let value: String

        init(_ c: ProcCulprit, value: String) {
            label = c.label
            title = c.title
            self.value = value
            let s = Second(path: c.pathParts, what: c.detail, parent: c.parentLabel)
            second = s.path == nil && s.what == nil && s.parent == nil ? nil : s
        }
    }

    let metric: Metric
    let value: String
    let tint: ThresholdTint
    /// "Peak 98% · Avg 71%"
    let detail: String?
    let chart: Chart
    let procs: Procs
    let footnote: String?

    @MainActor
    static func make(vm: ServerViewModel, metric: Metric) -> MetricHoverContent? {
        guard supports(metric) else { return nil }
        if metric == .disk { return makeDisk(vm: vm) }
        let series: MetricSeries
        let warn: Double
        let critical: Double
        let ranking: ProcCulprit.Ranking
        switch metric {
        case .cpu:
            (series, warn, critical, ranking) = (vm.cpu, vm.thresholds.cpuWarn, vm.thresholds.cpuCritical, .cpu)
        default:
            (series, warn, critical, ranking) = (vm.mem, vm.thresholds.memWarn, vm.thresholds.memCritical, .mem)
        }
        guard let latest = series.latest, let w = series.window(window) else { return nil }

        return MetricHoverContent(
            metric: metric,
            value: percentText(latest.v),
            tint: series.tint(warn: warn, critical: critical),
            detail: "Peak \(percentText(w.peak)) · Avg \(percentText(w.average))",
            chart: .percent(w.points, warn: warn),
            procs: procs(vm: vm, metric: metric, ranking: ranking, latest: latest),
            footnote: nil
        )
    }

    @MainActor
    private static func makeDisk(vm: ServerViewModel) -> MetricHoverContent? {
        guard let latest = vm.disk.latest else { return nil }
        let fullest = vm.latestDisks
            .filter { $0.total > 0 }
            .max { Double($0.used) / Double($0.total) < Double($1.used) / Double($1.total) }
        let free = fullest.map {
            "\(ProcessTable.formatBytes(max(0, $0.total - $0.used))) free of \(ProcessTable.formatBytes($0.total))"
        }
        let io = vm.diskIO.total
        let procs: Procs
        var footnote: String?
        if vm.hostOS == "darwin" {
            procs = .note("macOS does not report disk I/O per process.")
        } else {
            procs = ioProcs(vm: vm, latest: latest)
            if !vm.procsRoot {
                switch vm.hostOS {
                case "linux": footnote = "Other users need cap_sys_ptrace"
                case "windows": footnote = "Other users need elevation"
                default: break
                }
            }
        }
        return MetricHoverContent(
            metric: .disk,
            value: percentText(latest.v),
            tint: vm.disk.tint(warn: vm.thresholds.diskWarn, critical: vm.thresholds.diskCritical),
            detail: free,
            chart: .readWrite(
                read: io.read.window(window)?.points ?? [],
                write: io.write.window(window)?.points ?? []
            ),
            procs: procs,
            footnote: footnote
        )
    }

    private static func percentText(_ v: Double) -> String {
        "\(Int((v * 100).rounded()))%"
    }

    @MainActor
    private static func procs(
        vm: ServerViewModel, metric: Metric, ranking: ProcCulprit.Ranking, latest: MetricPoint
    ) -> Procs {
        if let n = vm.procsSkippedTotal { return .note("Too many processes to list (\(n)).") }
        guard let snap = vm.procs.latest, !snap.items.isEmpty,
              latest.t.timeIntervalSince(snap.t) <= staleProcs
        else { return .note("No process data.") }

        let top = ProcCulprit.top(snap.items, by: ranking, limit: topCount)
        let lines = top.map { Line($0, value: ranking.text($0)) }
        return .top(lines, others: others(vm: vm, metric: metric, latest: latest, top: top))
    }

    /// The top processes by disk I/O rate. Only processes with I/O show.
    @MainActor
    private static func ioProcs(vm: ServerViewModel, latest: MetricPoint) -> Procs {
        if let n = vm.procsSkippedTotal { return .note("Too many processes to list (\(n)).") }
        guard let snap = vm.procs.latest, !snap.items.isEmpty,
              latest.t.timeIntervalSince(snap.t) <= staleProcs
        else { return .note("No process data.") }
        let rates = vm.procs.ioRates()
        guard !rates.isEmpty else { return .note("No process I/O data.") }
        let ranking = ProcCulprit.Ranking.io(rates: rates)
        let top = ProcCulprit.top(snap.items, by: ranking, limit: topCount).filter { ranking.value($0) > 0 }
        guard !top.isEmpty else { return .note("No process disk I/O now.") }
        return .top(top.map { Line($0, value: ranking.text($0)) }, others: nil)
    }

    /// The part of the metric that the listed processes do not explain.
    @MainActor
    private static func others(vm: ServerViewModel, metric: Metric, latest: MetricPoint, top: [ProcCulprit]) -> String? {
        switch metric {
        case .cpu:
            guard let cores = vm.cpuCores else { return nil }
            let rest = max(0, latest.v * Double(cores) * 100 - top.reduce(0) { $0 + $1.cpuPct })
            return String(format: "%.0f%%", rest)
        case .disk, .net, .health:
            return nil
        default:
            guard let used = vm.memUsedBytes else { return nil }
            return ProcessTable.formatBytes(max(0, used - top.reduce(0) { $0 + $1.rss }))
        }
    }
}

struct MetricHoverCard: View {
    static let width: CGFloat = 300

    /// Opaque, so the rows below do not show through.
    private static let fill = Color(nsColor: NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(srgbRed: 0x26 / 255, green: 0x29 / 255, blue: 0x2D / 255, alpha: 1)
            : .white
    })

    let content: MetricHoverContent

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            header
            chart
            procs
            footer
        }
        .padding(.horizontal, 12)
        .padding(.top, 11)
        .padding(.bottom, 9)
        .frame(width: Self.width)
        .background(RoundedRectangle(cornerRadius: 10).fill(Self.fill))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.18), lineWidth: 1))
        .shadow(color: .black.opacity(0.45), radius: 14, y: 6)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    private var valueColor: Color {
        switch content.tint {
        case .warn, .critical: content.tint.color
        case .nominal, .stale: .primary
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(alignment: .firstTextBaseline) {
                Text(content.metric.displayName)
                    .font(Typography.rowColumn)
                    .tracking(1)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(content.value)
                    .font(.system(size: 17, weight: .semibold, design: .monospaced))
                    .foregroundStyle(valueColor)
            }
            if let detail = content.detail {
                Text(detail)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var chart: some View {
        switch content.chart {
        case .percent(let points, let warn):
            VStack(spacing: 3) {
                CanvasSparkline(
                    samples: points, tint: content.tint.color, warn: warn,
                    windowSeconds: MetricHoverContent.window, height: 40
                )
                axis
            }
        case .readWrite(let read, let write):
            let peak = max(read.map(\.v).max() ?? 0, write.map(\.v).max() ?? 0)
            let domain = 0...max(peak * 1.15, 0.01)
            VStack(spacing: 3) {
                HStack(spacing: 0) {
                    Text("I/O · ")
                    Text("READ \(ProcCulprit.Ranking.mbps((read.last?.v ?? 0) * 1_048_576))")
                        .foregroundStyle(RowsLayout.netRx)
                    Text(" · ")
                    Text("WRITE \(ProcCulprit.Ranking.mbps((write.last?.v ?? 0) * 1_048_576))")
                        .foregroundStyle(RowsLayout.netTx)
                    Text(" MB/s")
                    Spacer()
                }
                .font(.system(size: 9.5, design: .monospaced))
                .tracking(0.5)
                .foregroundStyle(.secondary)
                ZStack {
                    CanvasSparkline(
                        samples: read, tint: RowsLayout.netRx, warn: nil,
                        windowSeconds: MetricHoverContent.window, yDomain: domain, height: 40, showsArea: false
                    )
                    CanvasSparkline(
                        samples: write, tint: RowsLayout.netTx, warn: nil,
                        windowSeconds: MetricHoverContent.window, yDomain: domain, height: 40, showsArea: false
                    )
                }
                axis
            }
        }
    }

    private var axis: some View {
        HStack {
            Text("15 MIN AGO")
            Spacer()
            Text("NOW")
        }
        .font(.system(size: 9, design: .monospaced))
        .tracking(0.8)
        .foregroundStyle(.tertiary)
    }

    @ViewBuilder
    private var procs: some View {
        switch content.procs {
        case .top(let lines, _):
            VStack(alignment: .leading, spacing: 7) {
                if content.metric == .disk {
                    HStack {
                        Text("TOP PROCESSES")
                        Spacer()
                        Text("MB/s")
                    }
                    .font(.system(size: 9, design: .monospaced))
                    .tracking(0.8)
                    .foregroundStyle(.tertiary)
                }
                ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            Text(line.title)
                                .font(.system(size: 12, weight: .medium))
                                .lineLimit(1)
                                .truncationMode(.tail)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            Text(line.value)
                                .font(.system(size: 11.5, weight: .semibold, design: .monospaced))
                        }
                        if let second = line.second { secondRow(second) }
                    }
                }
            }
        case .note(let text):
            Text(text)
                .font(.system(size: 11.5))
                .italic()
                .foregroundStyle(.secondary)
        }
    }

    /// Path with the repo in bold, then what and the parent: "~/code/**sling**/web · vitest ← claude".
    private func secondRow(_ s: MetricHoverContent.Line.Second) -> some View {
        var tail = ""
        if let what = s.what { tail += " · " + what }
        if let parent = s.parent { tail += " ← " + parent }
        return HStack(spacing: 0) {
            if let p = s.path {
                (Text(p.prefix) + Text(p.repoName).bold().foregroundColor(.primary) + Text(p.subPath))
                    .lineLimit(1)
                    .truncationMode(.head)
            }
            if !tail.isEmpty {
                Text(s.path == nil ? String(tail.dropFirst(tail.hasPrefix(" · ") ? 3 : 1)) : tail)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .layoutPriority(1)
            }
        }
        .font(.system(size: 10.5, design: .monospaced))
        .foregroundStyle(.secondary)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let footnote = content.footnote {
                Text(footnote).font(.system(size: 10.5)).foregroundStyle(.secondary)
            }
            Divider().opacity(0.5)
            HStack {
                if case .top(_, let others?) = content.procs {
                    Text("Other processes: \(others)")
                }
                Spacer()
                Text("Click to open")
            }
            .font(.system(size: 10.5))
            .foregroundStyle(.tertiary)
        }
    }
}
