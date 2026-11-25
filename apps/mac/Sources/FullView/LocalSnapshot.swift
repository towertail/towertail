import Foundation

/// The process list of a local or SSH host at one time.
struct LocalSnapshot: Equatable {
    var rows: [ProcessSnapshotRow] = []
    var t: Date?
    var total = 0

    static let empty = LocalSnapshot()

    /// The snapshot nearest to `at`, or the newest when `at` is nil.
    /// Rows are sorted by `sort`. The I/O rate needs the snapshot before.
    init(series: ProcSeries, at: Date?, sort: ProcSort, limit: Int = 60) {
        guard let current = at.flatMap({ series.nearest(to: $0) }) ?? series.latest else { return }
        let previous = series.previous(before: current)
        let dt = previous.map { current.t.timeIntervalSince($0.t) }.flatMap { $0 > 0 ? $0 : nil }
        var prevByPid: [Int32: ProcSample] = [:]
        if let previous, dt != nil {
            for p in previous.items { prevByPid[p.pid] = p }
        }
        var all = current.items.map { p in
            ProcessSnapshotRow(ProcRow(from: p, previous: prevByPid[p.pid], dt: dt), sample: p)
        }
        switch sort {
        case .cpu: all.sort { $0.cpuPct > $1.cpuPct }
        case .rss: all.sort { $0.rss > $1.rss }
        case .io: all.sort { ($0.ioBps ?? -1, $0.cpuPct) > ($1.ioBps ?? -1, $1.cpuPct) }
        }
        rows = Array(all.prefix(limit))
        t = current.t
        total = all.count
    }

    private init() {}

    /// "14:02:11 · now" or "14:02:11 · 3 min ago".
    func label(now: Date) -> String {
        guard let t else { return "no snapshot" }
        let age = now.timeIntervalSince(t)
        let when = age < 15 ? "now" : ShortDuration.long(age) + " ago"
        return t.formatted(date: .omitted, time: .standard) + " · " + when
    }
}
