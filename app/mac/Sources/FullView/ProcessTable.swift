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
    /// Node the table is bound to. Needed by the kill flow so the dialog
    /// can show the exact command and dispatch it via SSH (or locally).
    /// Optional: older call sites / previews don't wire it up and simply
    /// hide the kill button.
    let node: Node?

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

    /// Row the user has chosen to kill; non-nil presents the confirmation
    /// dialog. Kept as the whole row so the dialog can show the name
    /// alongside the PID.
    @State private var killCandidate: ProcRow?
    /// Post-kill status for the inline toast. `.success` auto-dismisses;
    /// `.failure` requires the user to acknowledge so the stderr is seen.
    @State private var killResult: KillResult?
    /// In-flight flag so the confirmation dialog disables its own Kill
    /// button while the SSH/local process hasn't returned yet.
    @State private var killInFlight: Bool = false

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
            } else if displayed.isEmpty {
                unavailable("Waiting for first process sample…")
            } else {
                Table(displayed, sortOrder: $sort) {
                    TableColumn("PID", value: \.pid) { r in
                        PIDCell(
                            pid: r.pid,
                            name: r.name,
                            canKill: node != nil,
                            onKill: { killCandidate = r }
                        )
                    }
                    .width(min: 72, ideal: 88)

                    TableColumn("User", value: \.user) { r in
                        Text(r.user).lineLimit(1)
                            .textSelection(.enabled)
                    }
                    .width(min: 60, ideal: 90)

                    TableColumn("Name", value: \.name) { r in
                        Text(r.name).lineLimit(1)
                            .textSelection(.enabled)
                    }
                    .width(min: 100, ideal: 180)

                    TableColumn("CPU %", value: \.cpuPct) { r in
                        Text(String(format: "%.1f", r.cpuPct)).monospacedDigit()
                            .textSelection(.enabled)
                    }
                    .width(min: 60, ideal: 70)

                    TableColumn("RSS", value: \.rss) { r in
                        Text(Self.formatBytes(r.rss)).monospacedDigit()
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
        .confirmationDialog(
            killDialogTitle,
            isPresented: Binding(
                get: { killCandidate != nil },
                set: { if !$0 { killCandidate = nil } }
            ),
            titleVisibility: .visible,
            presenting: killCandidate
        ) { row in
            Button(role: .destructive) {
                runKill(row: row)
            } label: {
                Text(verbatim: "Kill \(row.pid)")
            }
            .disabled(killInFlight || node == nil)
            Button("Cancel", role: .cancel) { }
        } message: { row in
            Text(killDialogMessage(for: row))
        }
        .alert(
            killAlertTitle,
            isPresented: Binding(
                get: { killResult != nil },
                set: { if !$0 { killResult = nil } }
            ),
            presenting: killResult
        ) { _ in
            Button("OK", role: .cancel) { killResult = nil }
        } message: { result in
            switch result {
            case .success(let pid, let name):
                Text(verbatim: "Sent SIGKILL to \(name) (PID \(pid)).")
            case .failure(let message):
                Text(message)
            }
        }
    }

    // MARK: - Kill dialog + execution

    private var killDialogTitle: String {
        guard let row = killCandidate else { return "Kill process" }
        return "Kill \(row.name)?"
    }

    private var killAlertTitle: String {
        guard let r = killResult else { return "" }
        switch r {
        case .success: return "Process killed"
        case .failure: return "Kill failed"
        }
    }

    private func killDialogMessage(for row: ProcRow) -> String {
        let cmd = killCommandPreview(pid: row.pid)
        return "This will run:\n\n\(cmd)\n\nThe process will be terminated immediately with SIGKILL."
    }

    /// Shell preview for the confirmation dialog. Mirrors what the app
    /// will actually dispatch — including the remote invocation prefix —
    /// so the user sees the exact command that's about to run.
    private func killCommandPreview(pid: Int32) -> String {
        guard let node else { return "kill -9 \(pid)" }
        switch node.kind {
        case .local:
            return "kill -9 \(pid)"
        case .ssh:
            let user = node.sshUser ?? "?"
            let host = node.sshHost ?? "?"
            return "ssh \(user)@\(host) 'kill -9 \(pid)'"
        }
    }

    private func runKill(row: ProcRow) {
        guard let node else { return }
        killInFlight = true
        Task {
            let outcome = await ProcessKiller.kill(pid: row.pid, node: node)
            await MainActor.run {
                killInFlight = false
                killCandidate = nil
                switch outcome {
                case .success:
                    killResult = .success(pid: row.pid, name: row.name)
                case .failure(let message):
                    killResult = .failure(message: message)
                }
            }
        }
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

enum KillResult: Equatable {
    case success(pid: Int32, name: String)
    case failure(message: String)
}

/// Dispatches SIGKILL against a PID either locally via /bin/kill or
/// through the same SSH path the sampler uses. Shared module-level helper
/// rather than a method on ProcessTable so the table stays focused on
/// display and this logic can be tested / reused.
enum ProcessKiller {
    enum Outcome {
        case success
        case failure(message: String)
    }

    static func kill(pid: Int32, node: Node) async -> Outcome {
        switch node.kind {
        case .local:
            return await killLocal(pid: pid)
        case .ssh:
            return await killSSH(pid: pid, node: node)
        }
    }

    private static func killLocal(pid: Int32) async -> Outcome {
        do {
            let result = try await ProcessRunner.run(
                executable: URL(fileURLWithPath: "/bin/kill"),
                arguments: ["-9", String(pid)]
            )
            if result.exitCode == 0 { return .success }
            let err = String(data: result.stderr, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return .failure(message: err.isEmpty
                ? "kill exited \(result.exitCode)"
                : "kill exited \(result.exitCode): \(err)")
        } catch {
            return .failure(message: SamplerInvokeError.shortDescription(for: error))
        }
    }

    private static func killSSH(pid: Int32, node: Node) async -> Outcome {
        guard let user = node.sshUser, !user.isEmpty,
              let host = node.sshHost, !host.isEmpty else {
            return .failure(message: "SSH node missing user or host")
        }
        let args = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=5",
            "-o", "StrictHostKeyChecking=accept-new",
            "\(user)@\(host)",
            "kill -9 \(pid)"
        ]
        do {
            let result = try await ProcessRunner.run(
                executable: URL(fileURLWithPath: "/usr/bin/ssh"),
                arguments: args
            )
            if result.exitCode == 0 { return .success }
            let err = String(data: result.stderr, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return .failure(message: err.isEmpty
                ? "ssh exited \(result.exitCode)"
                : "ssh exited \(result.exitCode): \(err)")
        } catch {
            return .failure(message: SamplerInvokeError.shortDescription(for: error))
        }
    }
}
