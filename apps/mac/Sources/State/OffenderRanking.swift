import Foundation

/// One process group in the offenders table.
struct Offender: Identifiable, Sendable, Equatable {
    let ref: ProcessRef
    let avg: Double
    let peak: Double
    let rssMax: Int64
    /// Seconds with a CPU use of 50 % or more.
    let highFor: TimeInterval
    let series: [Double]

    var id: String { ref.key }
}

/// Ranks the processes of a window. Groups the PIDs of one command, averages
/// over all snapshots of the window and keeps `points` values for the chart.
struct OffenderRanking: Sendable {
    static let highCPU = 50.0

    private struct Acc {
        var ref: ProcessRef
        var sum = 0.0
        var peak = 0.0
        var rssMax: Int64 = 0
        var ioSum = 0.0
        var highFor = 0.0
        var perSnap: [Int: Double] = [:]
    }

    private let offenders: [Offender]

    init(snapshots: [ProcSeries.Snapshot], window: ClosedRange<Date>, by: ProcSort, points: Int = 60) {
        let snaps = snapshots.filter { window.contains($0.t) }.sorted { $0.t < $1.t }
        guard !snaps.isEmpty, points > 0 else {
            offenders = []
            return
        }
        let span = window.upperBound.timeIntervalSince(window.lowerBound)
        let step = span / Double(points)
        var groups: [String: Acc] = [:]
        for (i, snap) in snaps.enumerated() {
            let dt = i + 1 < snaps.count ? snaps[i + 1].t.timeIntervalSince(snap.t) : (i > 0 ? snap.t.timeIntervalSince(snaps[i - 1].t) : 0)
            let bucket = step > 0 ? min(points - 1, Int(snap.t.timeIntervalSince(window.lowerBound) / step)) : 0
            var perGroup: [String: (cpu: Double, rss: Int64, io: Double, ref: ProcessRef)] = [:]
            for p in snap.items {
                let ref = ProcessRef.local(p)
                var g = perGroup[ref.key] ?? (0, 0, 0, ref)
                g.cpu += p.cpuPct
                g.rss += p.rss
                g.io += Double(max(p.readBps ?? 0, 0) + max(p.writeBps ?? 0, 0))
                perGroup[ref.key] = g
            }
            for (key, g) in perGroup {
                var acc = groups[key] ?? Acc(ref: g.ref)
                acc.sum += g.cpu
                acc.ioSum += g.io
                acc.peak = max(acc.peak, g.cpu)
                acc.rssMax = max(acc.rssMax, g.rss)
                if g.cpu >= Self.highCPU { acc.highFor += dt }
                acc.perSnap[bucket] = max(acc.perSnap[bucket] ?? 0, g.cpu)
                groups[key] = acc
            }
        }
        let n = Double(snaps.count)
        let ranked = groups.values.map { acc -> (Offender, Double) in
            let avg = acc.sum / n
            let series = (0..<points).map { acc.perSnap[$0] ?? 0 }
            let key: Double
            switch by {
            case .cpu: key = avg
            case .rss: key = Double(acc.rssMax)
            case .io: key = acc.ioSum / n
            }
            return (Offender(ref: acc.ref, avg: avg, peak: acc.peak, rssMax: acc.rssMax, highFor: acc.highFor, series: series), key)
        }
        offenders = ranked
            .sorted { $0.1 != $1.1 ? $0.1 > $1.1 : $0.0.ref.key < $1.0.ref.key }
            .map(\.0)
    }

    func top(_ n: Int) -> [Offender] {
        Array(offenders.prefix(n))
    }
}
