import Foundation

/// Identity of one process across samples. A reused PID with a new
/// start time is a new key.
struct LongProcKey: Hashable, Sendable {
    let pid: Int32
    let start: Double?
    let name: String?

    init(_ p: ProcSample) {
        pid = p.pid
        start = p.startTS?.timeIntervalSince1970
        name = p.startTS == nil ? p.name : nil
    }
}

/// A process that stayed over the CPU limit for the sustain time.
struct LongProc: Sendable, Equatable, Identifiable {
    let key: LongProcKey
    let pid: Int32
    let name: String
    let cmd: String?
    /// Latest CPU, `top` convention.
    let cpuPct: Double
    let highSince: Date
    let startTS: Date?

    var id: LongProcKey { key }

    /// Name cut at 24 characters, the same as the notification labels.
    var shortName: String {
        let n = name.isEmpty ? "pid \(pid)" : name
        return n.count > 24 ? String(n.prefix(23)) + "…" : n
    }

    func highFor(at now: Date) -> TimeInterval { now.timeIntervalSince(highSince) }
}

/// Follows each process of one host over time. It stores only level
/// changes, so a 24 h window stays small.
struct LongProcTracker: Sendable {
    private struct Point: Sendable {
        let t: Date
        let high: Bool
    }

    private struct Entry: Sendable {
        var proc: ProcSample
        var points: [Point]
        var lastAt: Date
        var highSince: Date?
        var flagged = false
        /// False when the latest sample did not list the key. Such a key
        /// keeps its history but is not reported: it can be killed.
        var present = true
    }

    private var entries: [LongProcKey: Entry] = [:]

    var trackedCount: Int { entries.count }
    var changePointCount: Int { entries.values.reduce(0) { $0 + $1.points.count } }

    mutating func reset() { entries.removeAll() }

    /// Feeds one sample. `items == nil` or `skipped` records nothing, so
    /// the last level holds.
    mutating func observe(
        _ items: [ProcSample]?, skipped: Bool, at t: Date,
        rule: LongProcRule, ignored: Set<String>
    ) -> [LongProc] {
        guard rule.enabled else {
            entries.removeAll()
            return []
        }
        guard let items, !skipped else { return flagged() }
        if let latest = entries.values.map(\.lastAt).max(), t < latest { entries.removeAll() }

        var seen = Set<LongProcKey>()
        for p in items where !ignored.contains(p.name.lowercased()) {
            let key = LongProcKey(p)
            guard seen.insert(key).inserted else { continue }
            let high = p.cpuPct >= rule.cpuPct
            if var e = entries[key] {
                e.proc = p
                e.present = true
                record(&e, high: high, at: t, rule: rule)
                entries[key] = e
            } else if high {
                var e = Entry(proc: p, points: [], lastAt: t)
                record(&e, high: true, at: t, rule: rule)
                entries[key] = e
            }
        }
        for key in entries.keys where !seen.contains(key) {
            guard var e = entries[key] else { continue }
            if ignored.contains(e.proc.name.lowercased()) {
                entries[key] = nil
                continue
            }
            e.present = false
            record(&e, high: false, at: t, rule: rule)
            entries[key] = e
        }
        entries = entries.filter { $0.value.points.contains(where: \.high) }
        return flagged()
    }

    func flagged() -> [LongProc] {
        entries.compactMap { key, e -> LongProc? in
            guard e.flagged, e.present, let since = e.highSince else { return nil }
            return LongProc(
                key: key, pid: e.proc.pid, name: e.proc.name, cmd: e.proc.cmd,
                cpuPct: e.proc.cpuPct, highSince: since, startTS: e.proc.startTS
            )
        }
        .sorted { $0.cpuPct > $1.cpuPct }
    }

    private func record(_ e: inout Entry, high: Bool, at t: Date, rule: LongProcRule) {
        if e.points.last?.high != high { e.points.append(Point(t: t, high: high)) }
        e.lastAt = t
        if high, e.highSince == nil { e.highSince = t }
        let start = t.addingTimeInterval(-Double(rule.sustainSeconds))
        while e.points.count > 1, e.points[1].t <= start { e.points.removeFirst() }
        let full = e.points.first.map { $0.t <= start } ?? false
        e.flagged = full && Self.highFraction(e.points, from: start, to: t) >= LongProcRule.tolerance
        // The run ends when its own high fraction falls below the tolerance.
        if !e.flagged, let since = e.highSince {
            let from = max(since, start)
            if t > from, Self.highFraction(e.points, from: from, to: t) < LongProcRule.tolerance {
                e.highSince = high ? t : nil
            }
        }
    }

    /// Time-weighted high fraction over `start...now`. Each point holds
    /// its level until the next point.
    private static func highFraction(_ points: [Point], from start: Date, to now: Date) -> Double {
        let span = now.timeIntervalSince(start)
        guard span > 0 else { return 0 }
        var high = 0.0
        for (i, p) in points.enumerated() where p.high {
            let end = i + 1 < points.count ? points[i + 1].t : now
            let d = end.timeIntervalSince(max(p.t, start))
            if d > 0 { high += d }
        }
        return high / span
    }
}
