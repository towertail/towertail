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

                    TableColumn("Threads", value: \.threads) { r in
                        Text("\(r.threads)").monospacedDigit()
                    }
                    .width(min: 50, ideal: 70)
                }
                .onChange(of: sort) { _, newSort in
                    displayed.sort(using: newSort)
                }
                // Enough rows to usefully scan top processes without
                // crowding the chart. Fixed max so extra vertical space
                // goes to the chart (which has layoutPriority 1 above).
                .frame(minHeight: 220, maxHeight: 360)
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
        case .cpu, .disk, .net:
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
        let snap: [ProcSample]
        if let t {
            snap = series.nearest(to: t)?.items ?? []
        } else {
            snap = series.latest?.items ?? []
        }
        let rows = snap.map(ProcRow.init(from:))
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
}

/// Stable, sortable row value. `ProcSample` itself isn't Comparable on any
/// single field and `KeyPathComparator` needs concrete types, so we project.
struct ProcRow: Identifiable, Hashable {
    let id: Int32
    let pid: Int32
    let user: String
    let name: String
    let cpuPct: Double
    let rss: Int64
    let threads: Int32

    init(from p: ProcSample) {
        self.id = p.pid
        self.pid = p.pid
        self.user = p.user ?? "—"
        self.name = p.name
        self.cpuPct = p.cpuPct
        self.rss = p.rss
        self.threads = p.threads ?? 0
    }
}
