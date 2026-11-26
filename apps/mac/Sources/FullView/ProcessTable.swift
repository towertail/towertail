import SwiftUI

/// Renders the top-N process table for whatever timestamp the user has
/// effectively selected (hover ▸ pinned ▸ live-latest). Hover updates
/// arrive at ~30fps from the chart overlay; this view additionally
/// throttles the expensive snapshot lookup + SwiftUI Table diffing to
/// ~10fps so the table stays responsive without a long hover trail
/// causing visible lag.
struct ProcessTable: View {
    let series: ProcSeries
    let effectiveAt: Date?
    let available: Bool
    let isRootSampler: Bool
    /// Drives the default sort column. Clicking a header still wins.
    let sort: ProcSort
    /// Node the table is bound to. Needed by the kill flow so the dialog
    /// can show the exact command and dispatch it via SSH (or locally).
    /// Optional: older call sites / previews don't wire it up and simply
    /// hide the kill button.
    let node: Node?
    /// Process count when the latest sample skipped the per-process scan.
    var skippedTotal: Int? = nil
    /// Flagged rows get a clock badge.
    var longProcs: [LongProc] = []
    /// Nil hides the Ignore actions.
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil
    /// A single selection opens the process.
    var onOpen: ((ProcessRef) -> Void)? = nil
    @Binding var selection: Set<ProcRow.ID>

    @State private var sortOrder: [KeyPathComparator<ProcRow>] = [
        KeyPathComparator(\ProcRow.cpuPct, order: .reverse),
    ]
    /// Remembers the last sort that we seeded so we don't clobber a
    /// user-initiated sort on every re-render.
    @State private var lastAutoSort: ProcSort?

    @State private var displayed: [ProcRow] = []
    @State private var lastUpdateAt: Date = .distantPast
    @State private var lastRequestedT: Date?
    @State private var pendingTask: Task<Void, Never>?

    /// Row the user has chosen to kill. The kill flow shows the dialog.
    @State private var killCandidate: KillTarget?

    /// Target ≈ 5fps to match the chart's render throttle. Ingests can
    /// arrive every 0.5s (local) and each one walks the proc snapshot +
    /// rebuilds the Table; capping at 5Hz keeps both panels updating in
    /// lockstep without the table stuttering out of sync with the chart.
    private static let minUpdateInterval: TimeInterval = 0.2

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            header
            if !available {
                unavailable("Per-process collection disabled on this host.")
            } else if let n = skippedTotal {
                unavailable("Process scan skipped: \(HealthStatus.count(n)) processes (over the sampler's scan limit).")
            } else if displayed.isEmpty {
                unavailable("Waiting for first process sample…")
            } else {
                Table(displayed, selection: $selection, sortOrder: $sortOrder) {
                    TableColumn("PID", value: \.pid) { r in
                        PIDCell(
                            pid: r.pid,
                            name: r.name,
                            canKill: node != nil,
                            onKill: { killCandidate = KillTarget(pid: r.pid, name: r.name) }
                        )
                    }
                    .width(min: 72, ideal: 88)

                    TableColumn("User", value: \.user) { r in
                        Text(r.user).lineLimit(1)
                            .textSelection(.enabled)
                    }
                    .width(min: 60, ideal: 90)

                    TableColumn("Name", value: \.name) { r in
                        HStack(spacing: 4) {
                            if let p = longProcByPid[r.pid] {
                                Image(systemName: "clock")
                                    .foregroundStyle(ThresholdTint.warn.color)
                                    .help("High CPU for \(ShortDuration.long(p.highFor(at: Date())))")
                            }
                            Text(r.name).lineLimit(1)
                                .textSelection(.enabled)
                        }
                    }
                    .width(min: 100, ideal: 180)

                    TableColumn("CPU %", value: \.cpuPct) { r in
                        Text(String(format: "%.1f", r.cpuPct)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 60, ideal: 70)

                    TableColumn("RSS", value: \.rss) { r in
                        Text(ByteFormat.size(r.rss)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 60, ideal: 80)

                    TableColumn("Reads", value: \.readBps) { r in
                        Text(Self.formatIORate(r.readBps)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 80, ideal: 100)

                    TableColumn("Writes", value: \.writeBps) { r in
                        Text(Self.formatIORate(r.writeBps)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 80, ideal: 100)

                    TableColumn("Threads", value: \.threads) { r in
                        Text(verbatim: String(r.threads)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 50, ideal: 70)
                }
                .contextMenu(forSelectionType: ProcRow.ID.self) { ids in
                    if let row = displayed.first(where: { ids.contains($0.id) }) {
                        rowMenu(row)
                    }
                }
                .onChange(of: sortOrder) { _, newSort in
                    displayed.sort(using: newSort)
                }
                .onChange(of: selection) { _, ids in
                    guard ids.count == 1, let id = ids.first, let sample = sample(pid: id) else { return }
                    onOpen?(.local(sample))
                }
                // Enough rows to usefully scan top processes without
                // crowding the chart. Min kept small enough that a short
                // window still leaves room for the header + chart(s) +
                // toolbar; the table scrolls within this frame when the
                // row list doesn't fit.
                .frame(minHeight: 160, maxHeight: 360)
            }
        }
        .padding(.horizontal, 16)
        .onAppear {
            applyDefaultSort(for: sort)
            refresh(force: true)
        }
        .onChange(of: sort) { _, newSort in
            applyDefaultSort(for: newSort)
            refresh(force: true)
        }
        .onChange(of: effectiveAt) { _, _ in refresh(force: false) }
        .onDisappear { pendingTask?.cancel() }
        .processKillFlow(node: node, target: $killCandidate)
    }

    private func sample(pid: Int32) -> ProcSample? {
        let snap = effectiveAt.flatMap { series.nearest(to: $0) } ?? series.latest
        return snap?.items.first { $0.pid == pid }
    }

    private var longProcByPid: [Int32: LongProc] {
        Dictionary(longProcs.map { ($0.pid, $0) }, uniquingKeysWith: { a, _ in a })
    }

    @ViewBuilder
    private func rowMenu(_ row: ProcRow) -> some View {
        Button("Copy PID") { Pasteboard.copy(String(row.pid)) }
        if let sample = sample(pid: row.pid) {
            if let cmd = sample.cmd { Button("Copy command") { Pasteboard.copy(cmd) } }
            if let onOpen { Button("Open process") { onOpen(.local(sample)) } }
        }
        Divider()
        if let onIgnore {
            Button("Ignore \"\(row.name)\" on all hosts") { onIgnore(row.name, .allHosts) }
            if let node {
                Button("Ignore \"\(row.name)\" on this host") { onIgnore(row.name, .host(node.id)) }
            }
        }
        if node != nil {
            Divider()
            Button("Kill process…") { killCandidate = KillTarget(pid: row.pid, name: row.name) }
        }
    }

    /// Seeds the sort column from the process sort. Fires once per distinct
    /// sort so a click on a header sticks.
    private func applyDefaultSort(for sort: ProcSort) {
        guard lastAutoSort != sort else { return }
        lastAutoSort = sort
        switch sort {
        case .rss:
            sortOrder = [KeyPathComparator(\ProcRow.rss, order: .reverse)]
        case .io:
            // The total I/O rate falls back to CPU for rows without counters.
            sortOrder = [
                KeyPathComparator(\ProcRow.ioTotalBps, order: .reverse),
                KeyPathComparator(\ProcRow.cpuPct, order: .reverse),
            ]
        case .cpu:
            sortOrder = [KeyPathComparator(\ProcRow.cpuPct, order: .reverse)]
        }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Text("Processes").font(.headline)
            if isRootSampler {
                Label("root", systemImage: "shield.lefthalf.filled")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if available {
                Text("user scope")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help("Sampler running as a non-root user; on macOS this limits visibility to your own processes.")
            }
            Spacer()
            if let t = effectiveAt {
                Text(t.formatted(date: .omitted, time: .standard))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func unavailable(_ text: String) -> some View {
        VStack(spacing: 6) {
            Image(systemName: "list.bullet.rectangle")
                .font(.title2).foregroundStyle(.secondary)
            Text(text).font(.callout).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 120)
    }

    /// Pulls the nearest snapshot, but only commits a new table build if
    /// enough time has passed since the last one. When we defer, we
    /// schedule a trailing update so the final hover position is rendered.
    private func refresh(force: Bool) {
        let requested = effectiveAt
        lastRequestedT = requested

        let now = Date()
        if !force && now.timeIntervalSince(lastUpdateAt) < Self.minUpdateInterval {
            schedulePending(for: requested, in: Self.minUpdateInterval)
            return
        }
        commit(at: requested)
    }

    private func commit(at t: Date?) {
        let current: ProcSeries.Snapshot?
        if let t {
            current = series.nearest(to: t)
        } else {
            current = series.latest
        }
        guard let current else {
            displayed = []
            lastUpdateAt = Date()
            return
        }
        // Pair with the previous snapshot so we can turn the sampler's
        // cumulative read/write counters into a per-second rate. No
        // previous snapshot (or identical timestamp → dt==0) means the
        // first tick after launch — rates stay nil (rendered as "—") so
        // the user can tell "no history yet" from "genuinely idle".
        let previous = series.previous(before: current)
        let dt: TimeInterval? = {
            guard let p = previous else { return nil }
            let d = current.t.timeIntervalSince(p.t)
            return d > 0 ? d : nil
        }()
        var prevByPid: [Int32: ProcSample] = [:]
        if let previous, dt != nil {
            prevByPid.reserveCapacity(previous.items.count)
            for p in previous.items {
                prevByPid[p.pid] = p
            }
        }
        let rows = current.items.map { p in
            ProcRow(from: p, previous: prevByPid[p.pid], dt: dt)
        }
        let sortedRows: [ProcRow]
        if let cmp = sortOrder.first {
            sortedRows = rows.sorted(using: [cmp] + sortOrder.dropFirst())
        } else {
            sortedRows = rows
        }
        displayed = sortedRows
        lastUpdateAt = Date()
    }

    private func schedulePending(for t: Date?, in delay: TimeInterval) {
        pendingTask?.cancel()
        pendingTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            if Task.isCancelled { return }
            // Only fire if nothing more recent has superseded us. (The
            // moving hover will schedule newer tasks that cancel this one.)
            commit(at: lastRequestedT)
        }
    }

    /// Formats a per-process I/O rate. -1 is the "no previous snapshot /
    /// no sampler visibility" sentinel and renders as an em-dash so the
    /// user can tell "unknown" apart from "0 B/s idle".
    static func formatIORate(_ bps: Double) -> String {
        if bps < 0 { return "—" }
        if bps < 1 { return "0 B/s" }
        return ByteFormat.rate(bytesPerSec: bps)
    }
}

/// Stable, sortable row value. `ProcSample` itself isn't Comparable on any
/// single field and `KeyPathComparator` needs concrete types, so we project.
/// readBps/writeBps are the current rate in bytes/sec derived from the
/// sampler's lifetime counters (current - previous) / dt. -1 encodes
/// "no previous snapshot" or "no sampler visibility" so the table can
/// distinguish unknown from genuinely idle. Rows without data sort to the
/// bottom on the I/O columns since -1 is less than any real rate.
struct ProcRow: Identifiable, Hashable {
    let id: Int32
    let pid: Int32
    let user: String
    let name: String
    let cpuPct: Double
    let rss: Int64
    let threads: Int32
    let readBps: Double
    let writeBps: Double
    let ioTotalBps: Double

    init(from p: ProcSample, previous: ProcSample?, dt: TimeInterval?) {
        self.id = p.pid
        self.pid = p.pid
        self.user = p.user ?? "—"
        self.name = p.name
        self.cpuPct = p.cpuPct
        self.rss = p.rss
        self.threads = p.threads ?? 0

        // Rate computation: need current + previous + positive dt, and
        // the counter must not have decreased (PID reuse / sampler
        // restart resets the counter from the kernel's perspective).
        func rate(cur: Int64?, prev: Int64?) -> Double {
            guard let cur, let prev, let dt else { return -1 }
            if cur < 0 || prev < 0 { return -1 }
            let delta = cur - prev
            if delta < 0 { return -1 }
            return Double(delta) / dt
        }
        self.readBps = rate(cur: p.readBytes, prev: previous?.readBytes)
        self.writeBps = rate(cur: p.writeBytes, prev: previous?.writeBytes)
        // Treat missing-side as 0 contribution so a row with only reads
        // still sorts meaningfully on the hidden total.
        let r = max(self.readBps, 0)
        let w = max(self.writeBps, 0)
        self.ioTotalBps = r + w
    }
}

/// PID cell with a hover-revealed kill button. Kept as its own View
/// (rather than an inline @ViewBuilder that reads the parent's hover
/// state) so hover toggles only invalidate the hovered cell — not the
/// entire table. The previous inline implementation changed a shared
/// `@State hoveredPid` on the parent, which caused the whole Table
/// body to re-diff on every mouse movement and lagged visibly with
/// 50+ rows.
///
/// The button is always present in the layout; hover only changes its
/// opacity. Opacity changes don't invalidate layout, so the cell's
/// width stays stable and SwiftUI can skip most of the per-row work.
struct PIDCell: View {
    let pid: Int32
    let name: String
    let canKill: Bool
    let onKill: () -> Void
    @State private var hovering: Bool = false

    var body: some View {
        HStack(spacing: 4) {
            Text(verbatim: String(pid))
                .monospacedDigit()
                .textSelection(.enabled)
            CopyButton(text: String(pid), help: "Copy PID")
            if canKill {
                Button(action: onKill) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.red)
                }
                .buttonStyle(.plain)
                .help("Kill PID \(pid) (\(name))")
                .opacity(hovering ? 1 : 0)
                .allowsHitTesting(hovering)
                .pointingHandOnHover()
            }
            Spacer(minLength: 0)
        }
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
    }
}

enum Pasteboard {
    static func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }
}

/// Copies `text` and shows a checkmark for 1.5 s.
struct CopyButton: View {
    let text: String
    let help: String
    @State private var copied = false

    var body: some View {
        Button {
            Pasteboard.copy(text)
            copied = true
            Task {
                try? await Task.sleep(for: .seconds(1.5))
                copied = false
            }
        } label: {
            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                .font(.system(size: 10))
                .foregroundStyle(copied ? Color.green : Color.secondary)
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(help)
        .pointingHandOnHover()
    }
}
