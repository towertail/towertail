import SwiftUI

/// One alert line of the panel.
struct ProcessAlertLine: Identifiable, Equatable {
    let id: String
    let tint: ThresholdTint
    let text: String
    let detail: String
}

/// The panel at the right of the workbench: one process over the window.
struct ProcessPanel: View {
    @Bindable var model: ProcessPanelModel
    let cursor: Date?
    let cursorState: CursorState
    let window: ClosedRange<Date>
    let canKill: Bool
    let ignoredScope: LongProcIgnore.Scope?
    /// The host of the process. Nil hides "Ignore on this host".
    var hostId: UUID? = nil
    /// Hides "Compare with the host" in the Processes tab.
    var canCompare = true
    var hostCores: Int? = nil
    var hostMemTotal: Int64? = nil
    /// The long-running time of the process, when the alert is open.
    var longRunningFor: TimeInterval? = nil
    var bucketLabel = "each sample"
    var alerts: [ProcessAlertLine] = []
    var stopNote = "Stop process is only for local and SSH hosts. The sampler of a cloud host does not stop processes."
    let onIgnore: (String, LongProcIgnore.Scope) -> Void
    let onCompare: () -> Void
    let onKill: () -> Void
    let onClose: () -> Void

    static let width: CGFloat = 340

    private var ref: ProcessRef { model.ref }
    private var values: ProcessValues? { cursor.flatMap { model.values(at: $0) } ?? latestValues }

    /// With no cursor the panel shows the newest point.
    private var latestValues: ProcessValues? {
        model.detail?.cpu.last.flatMap { model.values(at: $0.t) }
    }

    var body: some View {
        VStack(spacing: 0) {
            head
            Divider().overlay(WB.border)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    commandBox
                    nowBlock
                    cpuBlock
                    memoryBlock
                    factsBlock
                    alertsBlock
                    actionsBlock
                }
                .padding(.horizontal, 14).padding(.vertical, 12)
            }
        }
        .frame(width: Self.width)
        .wbCard()
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Process \(ref.name)")
    }

    // MARK: Head

    private var head: some View {
        HStack(alignment: .center, spacing: 10) {
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 4) {
                    KLabel(ref.pid.map { "Process · pid \($0)" } ?? "Process")
                    if let pid = ref.pid { CopyButton(text: String(pid), help: "Copy PID") }
                }
                Text(ref.name)
                    .font(.system(size: 18, weight: .semibold))
                    .lineLimit(1).truncationMode(.tail)
            }
            Spacer(minLength: 4)
            Button(action: onClose) {
                Image(systemName: "xmark").font(.system(size: 12, weight: .medium)).foregroundStyle(WB.muted)
                    .frame(width: 24, height: 24).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help("Close (Esc)")
            .accessibilityLabel("Close the process panel")
            .pointingHandOnHover()
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
    }

    // MARK: Command

    @ViewBuilder
    private var commandBox: some View {
        if !ref.cmd.isEmpty {
            HStack(alignment: .top, spacing: 8) {
                Text(ref.cmd)
                    .font(.system(size: 12, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                CopyButton(text: ref.cmd, help: "Copy command")
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .background(WB.bg, in: RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(WB.border, lineWidth: 1))
        }
    }

    // MARK: At the cursor

    private var atLabel: String {
        let when = cursor.map { $0.formatted(date: .omitted, time: .standard) } ?? ""
        switch cursorState {
        case .hover: return "hover · " + when
        case .pinned: return "pinned · " + when
        case .live: return "now"
        }
    }

    private var nowBlock: some View {
        let v = values
        let running = v?.running ?? false
        return VStack(alignment: .leading, spacing: 6) {
            KLabel(atLabel).frame(height: 22, alignment: .leading)
            HStack(spacing: 6) {
                if running {
                    if let long = longRunningFor, cursorState == .live {
                        WBChip(text: "◷ long-running · " + ShortDuration.long(long), color: ThresholdTint.warn.color)
                    } else {
                        WBChip(text: "running", color: WB.cpu)
                    }
                } else if v != nil {
                    WBChip(text: "not running", color: WB.muted)
                }
                if ignoredScope != nil { WBChip(text: "ignored for long-running alerts", color: WB.muted) }
                WBChip(text: bucketLabel, color: WB.muted)
            }
            .lineLimit(1)
            .frame(height: 22, alignment: .leading)
            grid(v, running: running)
            if model.loading, model.detail == nil {
                ProgressView().controlSize(.small)
            } else if let e = model.error {
                Text(e).font(.system(size: 11)).foregroundStyle(ThresholdTint.warn.color)
            }
        }
    }

    private func grid(_ v: ProcessValues?, running: Bool) -> some View {
        let cpuSub: String = {
            guard running, let v else { return running ? " " : "not running" }
            var s = String(format: "%.2f cores", v.cpuPct / 100)
            if let c = hostCores, c > 0 { s += String(format: " · %.0f%%", v.cpuPct / Double(c)) }
            return s
        }()
        let memSub: String = {
            guard running, let v, let t = hostMemTotal, t > 0 else { return " " }
            return String(format: "%.1f%% of host", Double(v.rss) / Double(t) * 100)
        }()
        let started = model.detail?.facts.started
        let uptime = running ? started.map { "uptime " + ShortDuration.long((cursor ?? Date()).timeIntervalSince($0)) } : nil
        return LazyVGrid(columns: [GridItem(.flexible(), spacing: 8), GridItem(.flexible())], spacing: 8) {
            cell("CPU", running ? String(format: "%.1f%%", v?.cpuPct ?? 0) : "—", cpuSub, help: "Cores used · share of the host CPU")
            cell("Memory", running ? ProcessTable.formatBytes(v?.rss ?? 0) : "—", memSub)
            cell("Disk I/O", running ? (v?.ioBps.map(ProcessTable.formatIORate) ?? "—") : "—", running ? "read and write" : " ")
            cell("Threads", running ? (model.detail?.facts.threads.map(String.init) ?? "—") : "—", uptime ?? " ")
        }
    }

    private func cell(_ k: String, _ v: String, _ sub: String, help: String? = nil) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            KLabel(k)
            Text(v).font(.system(size: 17, weight: .semibold).monospacedDigit()).lineLimit(1)
            Text(sub).font(.system(size: 11, design: .monospaced)).foregroundStyle(WB.faint).lineLimit(1)
                .help(help ?? sub)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 10).padding(.vertical, 8)
        .background(WB.bg, in: RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(WB.border, lineWidth: 1))
    }

    // MARK: Charts

    private var cpuBlock: some View {
        VStack(alignment: .leading, spacing: 4) {
            KLabel("CPU over the window")
            if let s = model.cpuStats {
                Text(String(format: "avg %.0f%% · peak %.0f%% · ≥ 50%% for ", s.avg, s.peak)
                     + (s.highFor > 0 ? ShortDuration.long(s.highFor) : "0 s"))
                    .font(.system(size: 11, design: .monospaced)).foregroundStyle(WB.faint)
                    .lineLimit(1)
            }
            chart(name: "CPU", color: WB.cpu, points: model.detail?.cpu ?? [], axis: { "\(Int($0.rounded()))%" },
                  value: { String(format: "%.1f%%", $0) })
        }
    }

    private var memoryBlock: some View {
        VStack(alignment: .leading, spacing: 6) {
            KLabel("Memory over the window")
            chart(name: "Memory", color: WB.mem, points: model.detail?.rss ?? [],
                  axis: { ProcessTable.formatBytes(Int64($0)) }, value: { ProcessTable.formatBytes(Int64($0)) })
        }
    }

    private func chart(
        name: String, color: Color, points: [MetricPoint],
        axis: @escaping (Double) -> String, value: @escaping (Double) -> String
    ) -> some View {
        MetricChart(
            series: [ChartSeries(name: name, color: color, points: Self.decimated(points.filter { window.contains($0.t) }))], xDomain: window,
            warn: nil, critical: nil,
            hoverAt: cursorState == .hover ? cursor : nil, pinnedAt: cursorState == .pinned ? cursor : nil,
            yDomain: nil, yAxisLabel: axis, valueFormat: value, showsTooltip: false
        )
        .frame(height: 112)
    }

    /// Keeps the peak of each bucket, so the hover line does not redraw thousands of points.
    private static func decimated(_ pts: [MetricPoint], to limit: Int = 160) -> [MetricPoint] {
        guard pts.count > limit else { return pts }
        let size = Double(pts.count) / Double(limit)
        return (0..<limit).compactMap { i in
            let lo = Int(Double(i) * size), hi = min(pts.count, Int(Double(i + 1) * size))
            return pts[lo..<max(hi, lo + 1)].max { $0.v < $1.v }
        }
    }

    // MARK: Facts

    private static let factsNote = "The server keeps PID, parent and folder for 1 h. For older times the panel shows the last known values."

    private var factsBlock: some View {
        let f = model.detail?.facts ?? .empty
        let stale = model.detail != nil && (f.factsAt.map { Date().timeIntervalSince($0) > 3_600 } ?? true)
        return VStack(alignment: .leading, spacing: 6) {
            KLabel("Facts")
            Grid(alignment: .topLeading, horizontalSpacing: 12, verticalSpacing: 5) {
                row("User", f.user)
                GridRow {
                    label("Parent")
                    if let p = f.parent {
                        (Text(p) + Text(f.ppid.map { "  pid \($0)" } ?? "")
                            .font(.system(size: 11, design: .monospaced)).foregroundColor(WB.faint))
                            .font(.system(size: 12)).lineLimit(1)
                    } else {
                        dash
                    }
                }
                row("Started", f.started.map { $0.formatted(date: .abbreviated, time: .shortened) + " · " + ShortDuration.age(Date().timeIntervalSince($0)) + " ago" })
                row("Folder", f.cwd, mono: true)
                row("Repository", f.repo, mono: true)
                GridRow(alignment: .top) {
                    label("Ports")
                    if f.ports.isEmpty {
                        Text("none").font(.system(size: 12)).foregroundStyle(WB.faint)
                    } else {
                        VStack(alignment: .leading, spacing: 1) {
                            ForEach(f.ports, id: \.self) { Text($0).font(.system(size: 12, design: .monospaced)) }
                        }
                    }
                }
            }
            if stale {
                Text(Self.factsNote).font(.system(size: 11)).foregroundStyle(WB.faint)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func label(_ t: String) -> some View {
        Text(t).font(.system(size: 12)).foregroundStyle(WB.muted)
            .frame(width: 70, alignment: .leading)
    }

    private var dash: some View { Text("—").font(.system(size: 12)).foregroundStyle(WB.faint) }

    private func row(_ k: String, _ v: String?, mono: Bool = false) -> some View {
        GridRow(alignment: .top) {
            label(k)
            if let v, !v.isEmpty {
                Text(v)
                    .font(mono ? .system(size: 11.5, design: .monospaced) : .system(size: 12))
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                dash
            }
        }
    }

    // MARK: Alerts

    private var alertsBlock: some View {
        VStack(alignment: .leading, spacing: 6) {
            KLabel("Alerts · 7 days")
            if alerts.isEmpty {
                Text("No alerts for this process.").font(.system(size: 11)).foregroundStyle(WB.faint)
            } else {
                ForEach(alerts) { a in
                    HStack(alignment: .top, spacing: 8) {
                        Text(a.tint == .critical ? "CRITICAL" : "WARN")
                            .font(.system(size: 10, weight: .semibold, design: .monospaced))
                            .foregroundStyle(a.tint.color)
                            .padding(.horizontal, 5).padding(.vertical, 1)
                            .overlay(RoundedRectangle(cornerRadius: 4).stroke(a.tint.color, lineWidth: 1))
                        VStack(alignment: .leading, spacing: 1) {
                            Text(a.text).font(.system(size: 12))
                            Text(a.detail).font(.system(size: 11, design: .monospaced)).foregroundStyle(WB.faint)
                        }
                    }
                }
            }
        }
    }

    // MARK: Actions

    private var actionsBlock: some View {
        VStack(alignment: .leading, spacing: 6) {
            KLabel("Actions")
            FlowLayout(spacing: 6) {
                if let nodeScope = hostScope {
                    action("Ignore on this host", disabled: isIgnored(.host)) { onIgnore(ref.name, nodeScope) }
                }
                action("Ignore on all hosts", disabled: isIgnored(.all)) { onIgnore(ref.name, .allHosts) }
                if canCompare { action("Compare with the host", disabled: false, onCompare) }
                if canKill { action("Kill…", disabled: false, onKill, destructive: true) }
            }
            Text(stopNote).font(.system(size: 11)).foregroundStyle(WB.faint)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var hostScope: LongProcIgnore.Scope? { hostId.map { .host($0) } }

    private enum IgnoreKind { case host, all }

    private func isIgnored(_ kind: IgnoreKind) -> Bool {
        switch (ignoredScope, kind) {
        case (.allHosts?, _): return true
        case (.host?, .host): return true
        default: return false
        }
    }

    private func action(
        _ title: String, disabled: Bool, _ run: @escaping () -> Void, destructive: Bool = false
    ) -> some View {
        Button(action: run) {
            Text(title)
                .font(.system(size: 12, weight: .medium))
                .foregroundStyle(destructive ? Color.red : Color.primary)
                .padding(.horizontal, 10).padding(.vertical, 4)
                .background(WB.bg, in: RoundedRectangle(cornerRadius: 6))
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(WB.border, lineWidth: 1))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.4 : 1)
    }
}

/// Buttons that wrap to the next line.
private struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowH: CGFloat = 0, maxX: CGFloat = 0
        for s in subviews {
            let sz = s.sizeThatFits(.unspecified)
            if x > 0, x + sz.width > width { x = 0; y += rowH + spacing; rowH = 0 }
            x += sz.width + spacing
            rowH = max(rowH, sz.height)
            maxX = max(maxX, x - spacing)
        }
        return CGSize(width: maxX, height: y + rowH)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, rowH: CGFloat = 0
        for s in subviews {
            let sz = s.sizeThatFits(.unspecified)
            if x > bounds.minX, x + sz.width > bounds.maxX { x = bounds.minX; y += rowH + spacing; rowH = 0 }
            s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(sz))
            x += sz.width + spacing
            rowH = max(rowH, sz.height)
        }
    }
}
