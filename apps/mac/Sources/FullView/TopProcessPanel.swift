import SwiftUI

/// One row of the process snapshot at the cursor. Raw rows have a PID.
/// Aggregate rows (of a past window) have a peak.
struct ProcessSnapshotRow: Identifiable, Hashable, Sendable {
    let ref: ProcessRef
    let user: String?
    let cpuPct: Double
    let cpuPctMax: Double?
    let rss: Int64
    let ioBps: Double?
    let threads: Int32?
    let started: Date?

    /// Commands repeat, so a raw row needs its PID in the id.
    var id: String { ref.pid.map { "\($0)#\(ref.key)" } ?? ref.key }
    var isAggregate: Bool { cpuPctMax != nil }

    init(
        ref: ProcessRef, user: String? = nil, cpuPct: Double, cpuPctMax: Double? = nil,
        rss: Int64, ioBps: Double? = nil, threads: Int32? = nil, started: Date? = nil
    ) {
        self.ref = ref
        self.user = user
        self.cpuPct = cpuPct
        self.cpuPctMax = cpuPctMax
        self.rss = rss
        self.ioBps = ioBps
        self.threads = threads
        self.started = started
    }

    init(_ row: ProcRow, sample: ProcSample) {
        self.init(
            ref: .local(sample), user: sample.user, cpuPct: row.cpuPct, rss: row.rss,
            ioBps: row.readBps < 0 && row.writeBps < 0 ? nil : row.ioTotalBps,
            threads: sample.threads, started: sample.startTS
        )
    }
}

/// The top processes at the cursor: loading bar, state chip, sort, table.
struct TopProcessPanel: View {
    let rows: [ProcessSnapshotRow]
    let bucketLabel: String
    let state: CursorState
    let loading: Bool
    @Binding var sort: ProcSort
    let onOpen: (ProcessRef) -> Void
    /// Text that replaces the table, for a host with no process data.
    var placeholder: String? = nil
    /// "N of M processes" in the footer.
    var total: Int? = nil
    var node: Node? = nil
    var longProcs: [LongProc] = []
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil

    @State private var selection: Set<String> = []
    @State private var killTarget: KillTarget?

    var body: some View {
        VStack(spacing: 0) {
            loadBar
            head
            Divider()
            if let placeholder {
                Text(placeholder)
                    .font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                table.opacity(loading ? 0.5 : 1)
            }
            Divider()
            foot
        }
        .wbCard(fill: WB.sidebar)
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .processKillFlow(node: node, target: $killTarget)
    }

    private var loadBar: some View {
        ZStack(alignment: .leading) {
            Rectangle().fill(Color.clear)
            if loading {
                TimelineView(.animation) { ctx in
                    GeometryReader { geo in
                        let phase = ctx.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 0.9) / 0.9
                        Rectangle().fill(WB.info)
                            .frame(width: geo.size.width * 0.3)
                            .offset(x: (geo.size.width * 1.3) * phase - geo.size.width * 0.3)
                    }
                }
                .clipped()
            }
        }
        .frame(height: 2)
    }

    private var head: some View {
        HStack(spacing: 10) {
            KLabel("Top processes")
            Text(bucketLabel)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(WB.muted)
            chip
            Spacer()
            WBSegments(items: ProcSort.allCases, selection: $sort, label: \.displayName)
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
    }

    @ViewBuilder
    private var chip: some View {
        switch state {
        case .hover: WBChip(text: "hover", color: .primary)
        case .pinned: WBChip(text: "◆ pinned", color: WB.pin)
        case .live: WBChip(text: "live", color: WB.cpu)
        }
    }

    private var foot: some View {
        HStack(spacing: 10) {
            Text(total.map { "\(rows.count) of \($0) processes" } ?? "\(rows.count) processes")
            Spacer()
            HStack(spacing: 4) {
                Kbd("←"); Kbd("→"); Text("step when pinned")
            }
        }
        .font(.system(size: 11, design: .monospaced))
        .foregroundStyle(WB.faint)
        .padding(.horizontal, 14).padding(.vertical, 5)
    }

    private var longByPid: [Int32: LongProc] {
        Dictionary(longProcs.map { ($0.pid, $0) }, uniquingKeysWith: { a, _ in a })
    }

    @ViewBuilder
    private var table: some View {
        Group {
            if rows.first?.isAggregate == true {
                aggregateTable
            } else {
                rawTable
            }
        }
        .contextMenu(forSelectionType: String.self) { ids in
            if let row = rows.first(where: { ids.contains($0.id) }) { menu(row) }
        }
        .onChange(of: selection) { _, ids in
            if ids.count == 1, let row = rows.first(where: { ids.contains($0.id) }) { onOpen(row.ref) }
        }
    }

    private var rawTable: some View {
        Table(rows, selection: $selection) {
            TableColumn("PID") { r in
                if let pid = r.ref.pid {
                    PIDCell(pid: pid, name: r.ref.name, canKill: node != nil && node?.kind != .cloud) {
                        killTarget = KillTarget(pid: pid, name: r.ref.name)
                    }
                }
            }
            .width(min: 84, ideal: 96)
            TableColumn("Process") { r in nameCell(r) }
                .width(min: 140, ideal: 260)
            TableColumn("User") { r in Text(r.user ?? "—").lineLimit(1).foregroundStyle(.secondary) }
                .width(min: 50, ideal: 80)
            TableColumn("CPU %") { r in cpuCell(r.cpuPct, "%.1f") }
                .width(min: 70, ideal: 90)
            TableColumn("Memory") { r in Text(ProcessTable.formatBytes(r.rss)).monospacedDigit() }
                .width(min: 60, ideal: 80)
            TableColumn("I/O") { r in
                Text(r.ioBps.map(ProcessTable.formatIORate) ?? "—").monospacedDigit().foregroundStyle(.secondary)
            }
            .width(min: 70, ideal: 90)
            TableColumn("Threads") { r in
                Text(r.threads.map(String.init) ?? "—").monospacedDigit().foregroundStyle(.secondary)
            }
            .width(min: 50, ideal: 60)
            TableColumn("Started") { r in
                Text(r.started.map { ShortDuration.age(Date().timeIntervalSince($0)) + " ago" } ?? "—")
                    .foregroundStyle(.secondary)
            }
            .width(min: 60, ideal: 80)
        }
    }

    private var aggregateTable: some View {
        Table(rows, selection: $selection) {
            TableColumn("Process") { r in nameCell(r) }
                .width(min: 140, ideal: 280)
            TableColumn("CPU avg") { r in cpuCell(r.cpuPct, "%.1f%%") }
                .width(min: 80, ideal: 100)
            TableColumn("CPU peak") { r in
                Text(String(format: "%.1f%%", r.cpuPctMax ?? 0)).monospacedDigit().foregroundStyle(.secondary)
            }
            .width(min: 70, ideal: 90)
            TableColumn("Memory peak") { r in Text(ProcessTable.formatBytes(r.rss)).monospacedDigit() }
                .width(min: 80, ideal: 100)
            TableColumn("I/O avg") { r in
                Text(r.ioBps.map(ProcessTable.formatIORate) ?? "—").monospacedDigit().foregroundStyle(.secondary)
            }
            .width(min: 70, ideal: 90)
        }
    }

    private func nameCell(_ r: ProcessSnapshotRow) -> some View {
        HStack(spacing: 6) {
            if let pid = r.ref.pid, let p = longByPid[pid] {
                Image(systemName: "clock")
                    .foregroundStyle(ThresholdTint.warn.color)
                    .help("High CPU for \(ShortDuration.long(p.highFor(at: Date())))")
            }
            Text(r.ref.name).fontWeight(.semibold).lineLimit(1)
            if !r.ref.cmd.isEmpty, r.ref.cmd != r.ref.name {
                Text(r.ref.cmd)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(WB.faint)
                    .lineLimit(1)
            }
        }
    }

    private func cpuCell(_ v: Double, _ format: String) -> some View {
        let maxCPU = max(rows.map(\.cpuPct).max() ?? 1, 1)
        return HStack(spacing: 6) {
            RoundedRectangle(cornerRadius: 2).fill(WB.cpu.opacity(0.55))
                .frame(width: CGFloat(v / maxCPU) * 36, height: 5)
            Text(String(format: format, v)).monospacedDigit()
        }
    }

    @ViewBuilder
    private func menu(_ row: ProcessSnapshotRow) -> some View {
        Button("Open process") { onOpen(row.ref) }
        if let pid = row.ref.pid { Button("Copy PID") { Pasteboard.copy(String(pid)) } }
        if !row.ref.cmd.isEmpty { Button("Copy command") { Pasteboard.copy(row.ref.cmd) } }
        if let onIgnore {
            Divider()
            Button("Ignore \"\(row.ref.name)\" on all hosts") { onIgnore(row.ref.name, .allHosts) }
            if let node {
                Button("Ignore \"\(row.ref.name)\" on this host") { onIgnore(row.ref.name, .host(node.id)) }
            }
        }
        if let pid = row.ref.pid, node != nil, node?.kind != .cloud {
            Divider()
            Button("Kill process…") { killTarget = KillTarget(pid: pid, name: row.ref.name) }
        }
    }
}

/// A key cap, for the footer hints.
struct Kbd: View {
    let key: String
    init(_ key: String) { self.key = key }

    var body: some View {
        Text(key)
            .font(.system(size: 10.5, design: .monospaced))
            .foregroundStyle(WB.muted)
            .padding(.horizontal, 4)
            .overlay(RoundedRectangle(cornerRadius: 4).stroke(WB.border, lineWidth: 1))
    }
}
