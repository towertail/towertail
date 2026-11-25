import SwiftUI

/// The score, its history and the checks behind it.
struct HealthTab: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let frame: WorkbenchFrame
    let score: HealthScore
    var node: Node? = nil
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil

    var body: some View {
        ScrollView {
            VStack(spacing: 12) {
                HStack(alignment: .top, spacing: 12) {
                    ring.frame(width: 300)
                    scoreChart.frame(minHeight: 200)
                }
                .fixedSize(horizontal: false, vertical: true)
                checks
                if !vm.longProcs.isEmpty { longRunning }
            }
            .padding(.horizontal, 14).padding(.bottom, 12)
        }
    }

    private var pinned: Date? {
        if case .pinned(let t) = model.mode { return t }
        return nil
    }

    private var cursor: Date? { model.hoverAt ?? pinned }

    // MARK: Ring

    private var ring: some View {
        let value = frame.value(.health, at: cursor) ?? score.score
        let tint = HealthScore.tint(score: value)
        return VStack(alignment: .leading, spacing: 12) {
            KLabel("Health score")
            HStack(spacing: 16) {
                ZStack {
                    Circle().stroke(WB.border, lineWidth: 9)
                    Circle().trim(from: 0, to: max(0.01, value / 100))
                        .stroke(tint.color, style: StrokeStyle(lineWidth: 9, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                    VStack(spacing: 0) {
                        Text("\(Int(value.rounded()))")
                            .font(.system(size: 30, weight: .semibold).monospacedDigit())
                            .foregroundStyle(tint.valueColor)
                        Text("of 100").font(.system(size: 11, design: .monospaced)).foregroundStyle(WB.faint)
                    }
                }
                .frame(width: 100, height: 100)
                VStack(alignment: .leading, spacing: 4) {
                    Text(Self.word(tint)).font(.system(size: 15, weight: .semibold)).foregroundStyle(tint.valueColor)
                    Text(cursor == nil ? "now" : "at " + (cursor ?? Date()).formatted(date: .omitted, time: .standard))
                        .font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                }
            }
            VStack(spacing: 6) {
                ForEach(Array(score.parts.prefix(4)), id: \.check) { p in
                    HStack(spacing: 8) {
                        Text(p.label).font(.system(size: 12)).foregroundStyle(WB.muted)
                            .frame(width: 110, alignment: .leading).lineLimit(1)
                        bar(p)
                        Text("\(Int(p.score.rounded()))").font(.system(size: 12).monospacedDigit())
                            .frame(width: 28, alignment: .trailing)
                    }
                }
            }
        }
        .padding(14)
        .frame(maxHeight: .infinity, alignment: .top)
        .wbCard()
    }

    private static func word(_ t: ThresholdTint) -> String {
        switch t {
        case .nominal: return "healthy"
        case .warn: return "warn"
        case .critical: return "critical"
        case .stale: return "no data"
        }
    }

    private func bar(_ p: HealthScore.Part) -> some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(WB.border)
                Capsule().fill(p.tint.color).frame(width: geo.size.width * max(0.02, p.score / 100))
            }
        }
        .frame(height: 5)
    }

    // MARK: Score chart

    private var scoreChart: some View {
        let series = frame.series[.health] ?? []
        let lim = RailMetric.health.limits(vm.thresholds)
        return VStack(alignment: .leading, spacing: 6) {
            HStack {
                KLabel("Score over the window")
                Spacer()
                Text("warn \(Int(lim.warn ?? 0)) · critical \(Int(lim.critical ?? 0))")
                    .font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
            }
            MetricChart(
                series: series, xDomain: frame.window,
                warn: lim.warn, critical: lim.critical,
                hoverAt: model.hoverAt, pinnedAt: pinned,
                yDomain: 0...100, yAxisLabel: RailMetric.health.axisLabel, valueFormat: RailMetric.health.format,
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

    // MARK: Checks

    private var checks: some View {
        VStack(alignment: .leading, spacing: 0) {
            KLabel("Checks").padding(.horizontal, 14).padding(.vertical, 10)
            if score.parts.isEmpty {
                Text("No health data from this host's sampler.")
                    .font(.callout).foregroundStyle(.secondary)
                    .padding(.horizontal, 14).padding(.bottom, 12)
            }
            ForEach(Array(score.parts.enumerated()), id: \.element.check) { _, p in
                Divider().overlay(WB.border)
                checkRow(p)
            }
        }
        .wbCard()
    }

    private func checkRow(_ p: HealthScore.Part) -> some View {
        HStack(spacing: 10) {
            Circle().fill(p.tint.color).frame(width: 8, height: 8)
            Text(p.label).font(.system(size: 13)).frame(width: 150, alignment: .leading).lineLimit(1)
            Text(valueText(p)).font(.system(size: 13, weight: .medium).monospacedDigit())
                .foregroundStyle(p.tint.valueColor)
                .frame(width: 130, alignment: .leading).lineLimit(1)
            spark(p).frame(width: 120, height: 22)
            Text(limitText(p)).font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                .lineLimit(1)
            Spacer(minLength: 8)
            Text("\(Int(p.score.rounded()))").font(.system(size: 12).monospacedDigit()).foregroundStyle(WB.muted)
            bar(p).frame(width: 70)
        }
        .padding(.horizontal, 14).padding(.vertical, 7)
    }

    /// Only the checks with a series draw a sparkline.
    @ViewBuilder
    private func spark(_ p: HealthScore.Part) -> some View {
        let span = frame.window.upperBound.timeIntervalSince(frame.window.lowerBound)
        let source: (pts: [MetricPoint], domain: ClosedRange<Double>?)? = {
            switch p.check {
            case .cpu: return (frame.points(.cpu), 0...1)
            case .mem: return (frame.points(.mem), 0...1)
            case .disk: return (frame.points(.disk), 0...1)
            case .signal(let sig):
                let (name, domain): (String, ClosedRange<Double>?) = {
                    switch sig {
                    case .procs: return ("procs", nil)
                    case .zombies: return ("zombies", nil)
                    case .pids: return ("pids", 0...1)
                    case .files: return ("files", 0...1)
                    case .inodes: return ("inodes", 0...1)
                    case .memPressure: return (vm.hostOS == "linux" ? "psi_mem" : "mem_pressure", 0...100)
                    case .ioPressure: return ("psi_io_full", 0...100)
                    case .longProc: return ("", nil)
                    }
                }()
                let pts = frame.named[name] ?? []
                return pts.isEmpty ? nil : (pts, domain ?? 0...max(1, (pts.map(\.v).max() ?? 1) * 1.15))
            }
        }()
        if let source {
            CanvasSparkline(
                samples: source.pts, tint: p.tint.color, warn: nil, windowSeconds: span,
                yDomain: source.domain, height: 22, xDomain: frame.window
            )
        } else {
            Color.clear
        }
    }

    private func valueText(_ p: HealthScore.Part) -> String {
        switch p.check {
        case .cpu, .mem, .disk:
            let m: RailMetric = p.check == .cpu ? .cpu : p.check == .mem ? .mem : .disk
            return m.format(frame.value(m, at: cursor) ?? p.value)
        case .signal(let s):
            switch s {
            case .procs, .zombies, .longProc: return HealthStatus.count(Int(p.value))
            case .pids, .files, .inodes: return percent(p.value)
            case .memPressure:
                if let h = vm.health.info, h.memPressurePct == nil, h.psi == nil, let lvl = h.memPressure {
                    return lvl == 4 ? "critical" : lvl == 2 ? "high" : "normal"
                }
                return String(format: "%.0f%%", p.value)
            case .ioPressure: return String(format: "%.0f%%", p.value)
            }
        }
    }

    private func limitText(_ p: HealthScore.Part) -> String {
        guard let l = p.limit else {
            if case .signal(.longProc) = p.check { return "over the CPU rule" }
            return ""
        }
        func f(_ v: Double) -> String {
            switch p.check {
            case .cpu, .mem, .disk, .signal(.pids), .signal(.files), .signal(.inodes): return percent(v)
            case .signal(.memPressure), .signal(.ioPressure): return String(format: "%.0f%%", v)
            case .signal: return HealthStatus.count(Int(v))
            }
        }
        if case .signal(.memPressure) = p.check, vm.health.info?.memPressurePct == nil, vm.health.info?.psi == nil {
            return ""
        }
        return "warn \(f(l.warn)) · critical \(f(l.critical))"
    }

    private func percent(_ ratio: Double) -> String { "\(Int((ratio * 100).rounded()))%" }

    // MARK: Long-running processes

    private var longRunning: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                KLabel("Long-running processes")
                Spacer()
                if !vm.ignoredProcs.isEmpty {
                    Text("ignored here: " + vm.ignoredProcs.sorted().joined(separator: ", "))
                        .font(.system(size: 11, design: .monospaced)).foregroundStyle(WB.faint).lineLimit(1)
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            ForEach(vm.longProcs) { p in
                Divider().overlay(WB.border)
                HStack(spacing: 12) {
                    Text(p.name).font(.system(size: 13, weight: .medium)).lineLimit(1)
                        .frame(width: 180, alignment: .leading).help(p.cmd ?? p.name)
                    Text(verbatim: "pid \(p.pid)").font(.system(size: 12, design: .monospaced)).foregroundStyle(WB.faint)
                    Text("\(Int(p.cpuPct.rounded()))% CPU").font(.system(size: 12).monospacedDigit())
                    Text("high for " + ShortDuration.long(p.highFor(at: Date())))
                        .font(.system(size: 12).monospacedDigit()).foregroundStyle(WB.muted)
                    Spacer()
                    Menu {
                        Button("Ignore \"\(p.name)\" on all hosts") { onIgnore?(p.name, .allHosts) }
                        if let node {
                            Button("Ignore \"\(p.name)\" on this host") { onIgnore?(p.name, .host(node.id)) }
                        }
                        if let cmd = p.cmd { Divider(); Button("Copy command") { Pasteboard.copy(cmd) } }
                    } label: { Text("Ignore") }
                    .menuStyle(.borderlessButton).fixedSize().controlSize(.small)
                    .disabled(onIgnore == nil)
                }
                .padding(.horizontal, 14).padding(.vertical, 7)
                .contentShape(Rectangle())
                .onTapGesture {
                    // A Cloud row carries the key of the server.
                    let live = vm.procs.latest?.items.first { $0.pid == p.pid && $0.name == p.name }
                    model.openProcess = live.map(ProcessRef.local)
                        ?? ProcessRef(key: ProcessRef.key(name: p.name, cmd: p.cmd), pid: p.pid, name: p.name, cmd: p.cmd ?? "")
                }
            }
        }
        .wbCard()
    }
}
