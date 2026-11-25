import Foundation

/// Where the process panel gets its series and facts.
@MainActor
protocol ProcessDetailSource: AnyObject {
    func detail(of ref: ProcessRef, window: ClosedRange<Date>) async throws -> ProcessDetail
}

struct ProcessFacts: Sendable, Equatable {
    var user: String?
    var ppid: Int32?
    var parent: String?
    var cwd: String?
    var repo: String?
    var ports: [String] = []
    var started: Date?
    /// The time of the sample that the facts come from.
    var factsAt: Date?
    var threads: Int32?

    static let empty = ProcessFacts()
}

struct ProcessDetail: Sendable, Equatable {
    var facts: ProcessFacts
    /// Percent of one core, `top` convention.
    var cpu: [MetricPoint]
    /// Bytes.
    var rss: [MetricPoint]
    /// Bytes per second, read and write together. Empty with no I/O data.
    var io: [MetricPoint]
}

struct ProcessValues: Equatable, Sendable {
    let cpuPct: Double
    let rss: Int64
    let ioBps: Double?
    let running: Bool
}

/// The CPU figures of the window, for the head of the CPU chart.
struct ProcessCPUStats: Equatable, Sendable {
    let avg: Double
    let peak: Double
    /// Seconds at or over `ProcessPanelModel.highCPU`.
    let highFor: TimeInterval
}

/// The detail of one process. A cursor move reads the loaded series and sends no request.
@MainActor
@Observable
final class ProcessPanelModel {
    static let highCPU = 50.0

    let ref: ProcessRef
    private(set) var detail: ProcessDetail?
    private(set) var loading = false
    private(set) var error: String?
    @ObservationIgnored private let source: any ProcessDetailSource

    init(ref: ProcessRef, source: any ProcessDetailSource) {
        self.ref = ref
        self.source = source
    }

    func load(window: ClosedRange<Date>) async {
        loading = true
        defer { loading = false }
        do {
            let d = try await source.detail(of: ref, window: window)
            guard !Task.isCancelled else { return }
            detail = d
            error = nil
        } catch {
            guard !Task.isCancelled else { return }
            self.error = error.localizedDescription
        }
    }

    /// The values at `t`. A time outside the points of the process gives `running == false`.
    func values(at t: Date) -> ProcessValues? {
        guard let detail else { return nil }
        let cpu = detail.cpu
        guard let c = MetricChart.nearest(in: cpu, to: t), abs(c.t.timeIntervalSince(t)) <= tolerance(cpu) else {
            return ProcessValues(cpuPct: 0, rss: 0, ioBps: nil, running: false)
        }
        let rss = MetricChart.nearest(in: detail.rss, to: t)?.v ?? 0
        let io = MetricChart.nearest(in: detail.io, to: t).flatMap {
            abs($0.t.timeIntervalSince(t)) <= tolerance(detail.io) ? $0.v : nil
        }
        return ProcessValues(cpuPct: c.v, rss: Int64(rss), ioBps: io, running: true)
    }

    var cpuStats: ProcessCPUStats? {
        guard let pts = detail?.cpu, !pts.isEmpty else { return nil }
        let avg = pts.map(\.v).reduce(0, +) / Double(pts.count)
        let step = typicalStep(pts)
        var high = 0.0
        for (i, p) in pts.enumerated() where p.v >= Self.highCPU {
            let next = i + 1 < pts.count ? pts[i + 1].t.timeIntervalSince(p.t) : step
            high += min(next, step * 2)
        }
        return ProcessCPUStats(avg: avg, peak: pts.map(\.v).max() ?? 0, highFor: high)
    }

    /// How far the cursor may be from a point and still count as running.
    private func tolerance(_ pts: [MetricPoint]) -> TimeInterval {
        max(typicalStep(pts) * 1.5, 2)
    }

    /// The median gap between points.
    private func typicalStep(_ pts: [MetricPoint]) -> TimeInterval {
        guard pts.count >= 2 else { return 10 }
        let gaps = zip(pts, pts.dropFirst()).map { $1.t.timeIntervalSince($0.t) }.sorted()
        return gaps[gaps.count / 2]
    }
}

/// Builds the detail of a local or SSH host from the live process series.
@MainActor
final class LocalProcessSource: ProcessDetailSource {
    private let vm: ServerViewModel

    init(vm: ServerViewModel) {
        self.vm = vm
    }

    func detail(of ref: ProcessRef, window: ClosedRange<Date>) async throws -> ProcessDetail {
        var cpu: [MetricPoint] = []
        var rss: [MetricPoint] = []
        var io: [MetricPoint] = []
        var newest: (t: Date, items: [ProcSample])?
        for snap in vm.procs.snapshots(in: window).sorted(by: { $0.t < $1.t }) {
            let hits = snap.items.filter { ProcessRef.key(of: $0) == ref.key }
            guard !hits.isEmpty else { continue }
            cpu.append(MetricPoint(t: snap.t, v: hits.reduce(0) { $0 + $1.cpuPct }))
            rss.append(MetricPoint(t: snap.t, v: Double(hits.reduce(Int64(0)) { $0 + $1.rss })))
            if hits.contains(where: { $0.readBps != nil && $0.writeBps != nil }) {
                let total = hits.reduce(0.0) { $0 + Double(max($1.readBps ?? 0, 0) + max($1.writeBps ?? 0, 0)) }
                io.append(MetricPoint(t: snap.t, v: total))
            }
            newest = (snap.t, hits)
        }
        return ProcessDetail(facts: facts(from: newest, ref: ref), cpu: cpu, rss: rss, io: io)
    }

    private func facts(from newest: (t: Date, items: [ProcSample])?, ref: ProcessRef) -> ProcessFacts {
        guard let newest else { return .empty }
        let sample = newest.items.first { $0.pid == ref.pid } ?? newest.items[0]
        var ports: [String] = []
        for item in vm.ports?.items ?? [] where newest.items.contains(where: { $0.pid == item.pid }) {
            ports += (item.listenTCP ?? []).map { "tcp \($0)" }
            ports += (item.listenUDP ?? []).map { "udp \($0)" }
        }
        return ProcessFacts(
            user: sample.user, ppid: sample.ppid, parent: sample.parent, cwd: sample.cwd, repo: sample.repo,
            ports: Array(Set(ports)).sorted(), started: sample.startTS, factsAt: newest.t, threads: sample.threads
        )
    }
}
