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
    /// The metric the full view is currently focused on. Drives the table's
    /// default sort column so "MEM" surfaces the RAM hogs without the user
    /// having to click the RSS header.
    let metric: Metric

    @State private var sort: [KeyPathComparator<ProcRow>] = [
        KeyPathComparator(\ProcRow.cpuPct, order: .reverse),
    ]
    /// Remembers the last metric we auto-seeded from so we don't clobber a
    /// user-initiated sort on every re-render.
    @State private var lastAutoSortedMetric: Metric?

    @State private var displayed: [ProcRow] = []
    @State private var lastUpdateAt: Date = .distantPast
    @State private var lastRequestedT: Date?
    @State private var pendingTask: Task<Void, Never>?

    /// Target ≈ 10fps. The chart already throttles hover emits to 30fps
    /// and most of those fall within the same nearest-snapshot slot, so
    /// in practice we rebuild the table more like 2-3 times per second
    /// while the user drags across the timeline.
    private static let minUpdateInterval: TimeInterval = 0.1

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            header
            if !available {
                unavailable("Per-process collection disabled on this host.")
            } else if displayed.isEmpty {
                unavailable("Waiting for first process sample…")
            } else {
                Table(displayed, sortOrder: $sort) {
                    TableColumn("PID", value: \.pid) { r in
                        Text("\(r.pid)").monospacedDigit()
                    }
                    .width(min: 48, ideal: 60)

                    TableColumn("User", value: \.user) { r in
                        Text(r.user).lineLimit(1)
                    }
                    .width(min: 60, ideal: 90)

                    TableColumn("Name", value: \.name) { r in
                        Text(r.name).lineLimit(1)
                    }
                    .width(min: 100, ideal: 180)

                    TableColumn("CPU %", value: \.cpuPct) { r in
                        Text(String(format: "%.1f", r.cpuPct)).monospacedDigit()
                    }
                    .width(min: 60, ideal: 70)

                    TableColumn("RSS", value: \.rss) { r in
                        Text(Self.formatBytes(r.rss)).monospacedDigit()
                    }
                    .width(min: 60, ideal: 80)

                    TableColumn("Reads", value: \.readBps) { r in
                        Text(Self.formatIORate(r.readBps)).monospacedDigit()
                    }
                    .width(min: 80, ideal: 100)

                    TableColumn("Writes", value: \.writeBps) { r in
                        Text(Self.formatIORate(r.writeBps)).monospacedDigit()
                    }
                    .width(min: 80, ideal: 100)

                    TableColumn("Threads", value: \.threads) { r in
                        Text("\(r.threads)").monospacedDigit()
                    }
                    .width(min: 50, ideal: 70)
                }
                .onChange(of: sort) { _, newSort in
                    displayed.sort(using: newSort)
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
            applyDefaultSort(for: metric)
            refresh(force: true)
        }
        .onChange(of: metric) { _, newMetric in
            applyDefaultSort(for: newMetric)
            refresh(force: true)
        }
        .onChange(of: effectiveAt) { _, _ in refresh(force: false) }
        .onDisappear { pendingTask?.cancel() }
    }

    /// Seeds the sort column based on the active metric. Only fires once per
    /// distinct metric so clicking a header still sticks while the user
    /// stays on the same tab.
    private func applyDefaultSort(for metric: Metric) {
        guard lastAutoSortedMetric != metric else { return }
        lastAutoSortedMetric = metric
        switch metric {
        case .mem:
            sort = [KeyPathComparator(\ProcRow.rss, order: .reverse)]
        case .disk:
            // Surface biggest disk-I/O contributors right now. ioTotalBps
            // is the hidden read+write rate; falls back to cpuPct for rows
            // where the sampler couldn't read per-proc counters (macOS, or
            // Linux without CAP_SYS_PTRACE).
            sort = [
                KeyPathComparator(\ProcRow.ioTotalBps, order: .reverse),
                KeyPathComparator(\ProcRow.cpuPct, order: .reverse),
            ]
        case .cpu, .net:
            sort = [KeyPathComparator(\ProcRow.cpuPct, order: .reverse)]
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
        if let cmp = sort.first {
            sortedRows = rows.sorted(using: [cmp] + sort.dropFirst())
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

    static func formatBytes(_ b: Int64) -> String {
        let v = Double(max(b, 0))
        if v < 1024 { return String(format: "%.0f B", v) }
        if v < 1_048_576 { return String(format: "%.0f KB", v / 1024) }
        if v < 1_073_741_824 { return String(format: "%.1f MB", v / 1_048_576) }
        return String(format: "%.2f GB", v / 1_073_741_824)
    }

    /// Formats a per-process I/O rate. -1 is the "no previous snapshot /
    /// no sampler visibility" sentinel and renders as an em-dash so the
    /// user can tell "unknown" apart from "0 B/s idle".
    static func formatIORate(_ bps: Double) -> String {
        if bps < 0 { return "—" }
        let v = max(0, bps)
        if v < 1 { return "0 B/s" }
        if v < 1_024 { return String(format: "%.0f B/s", v) }
        if v < 1_048_576 { return String(format: "%.1f KB/s", v / 1_024) }
        if v < 1_073_741_824 { return String(format: "%.1f MB/s", v / 1_048_576) }
        return String(format: "%.2f GB/s", v / 1_073_741_824)
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
