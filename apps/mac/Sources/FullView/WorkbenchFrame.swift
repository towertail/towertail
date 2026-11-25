import SwiftUI

extension RailMetric {
    var color: Color {
        switch self {
        case .cpu: return WB.cpu
        case .mem: return WB.mem
        case .load: return WB.load
        case .disk: return WB.disk
        case .net: return WB.netIn
        case .io: return WB.read
        case .health: return WB.health
        }
    }

    /// The chart scale. Nil scales to the data.
    var yDomain: ClosedRange<Double>? {
        switch self {
        case .cpu, .mem, .disk: return 0...1
        case .health: return 0...100
        case .load, .net, .io: return nil
        }
    }

    /// Net and I/O draw two lines.
    var hasTwoSeries: Bool { self == .net || self == .io }

    func format(_ v: Double) -> String {
        switch self {
        case .cpu, .mem, .disk: return "\(Int((v * 100).rounded()))%"
        case .load: return String(format: "%.2f", v)
        case .net, .io: return FullViewWindow.formatByteRate(bytesPerSec: v * 1_048_576)
        case .health: return "\(Int(v.rounded()))"
        }
    }

    func axisLabel(_ v: Double) -> String {
        switch self {
        case .load: return String(format: "%.1f", v)
        default: return format(v)
        }
    }

    /// Warn and critical lines of the chart, in the scale of the series.
    func limits(_ t: MetricThresholds) -> (warn: Double?, critical: Double?) {
        switch self {
        case .cpu: return (t.cpuWarn, t.cpuCritical)
        case .mem: return (t.memWarn, t.memCritical)
        case .disk: return (t.diskWarn, t.diskCritical)
        case .health: return (80, 40)
        case .load, .net, .io: return (nil, nil)
        }
    }

    func tint(of v: Double, thresholds t: MetricThresholds) -> ThresholdTint {
        switch self {
        case .cpu, .mem, .disk:
            let l = limits(t)
            if let c = l.critical, v >= c { return .critical }
            if let w = l.warn, v >= w { return .warn }
            return .nominal
        case .health: return HealthScore.tint(score: v)
        case .load, .net, .io: return .nominal
        }
    }
}

/// The series of the rail for one window, built at a low rate so that
/// the charts do not redraw on each sample.
struct WorkbenchFrame: Equatable {
    var window: ClosedRange<Date>
    var series: [RailMetric: [ChartSeries]]
    /// Time of the newest sample.
    var latest: Date?
    /// Time of the oldest point that the host has.
    var oldest: Date?
    /// Series of the health checks, by the history name (`procs`, `zombies`, ...).
    var named: [String: [MetricPoint]] = [:]

    static let empty = WorkbenchFrame(
        window: Date()...Date(), series: [:], latest: nil, oldest: nil
    )

    /// A Cloud host: the REST series of the window, then the live points.
    @MainActor
    init(vm: ServerViewModel, window: ClosedRange<Date>, cloud: WorkbenchSeries) {
        self.window = window
        func pts(_ name: String, _ live: MetricSeries?) -> [MetricPoint] {
            cloud.points(name, live: live).filter { window.contains($0.t) }
        }
        func one(_ name: String, _ color: Color, _ points: [MetricPoint]) -> [ChartSeries] {
            [ChartSeries(name: name, color: color, points: points)]
        }
        let linux = vm.hostOS == "linux"
        let rest = cloud.scorePoints(thresholds: vm.thresholds, muted: vm.mutedSignals, linux: linux)
        let score = (rest + vm.healthScoreSeries.snapshot().filter { $0.t > (rest.last?.t ?? .distantPast) })
            .filter { window.contains($0.t) }
        var out: [RailMetric: [ChartSeries]] = [:]
        out[.cpu] = one("CPU", WB.cpu, pts("cpu", vm.cpu))
        out[.mem] = one("Memory", WB.mem, pts("mem", vm.mem))
        out[.load] = one("Load", WB.load, pts("load", vm.load))
        out[.disk] = one("Disk", WB.disk, pts("disk", vm.disk))
        out[.net] = [
            ChartSeries(name: "In", color: WB.netIn, points: pts("net_rx", vm.netRx)),
            ChartSeries(name: "Out", color: WB.netOut, points: pts("net_tx", vm.netTx)),
        ]
        out[.io] = [
            ChartSeries(name: "Read", color: WB.read, points: pts("disk_read", vm.diskIO.total.read)),
            ChartSeries(name: "Write", color: WB.write, points: pts("disk_write", vm.diskIO.total.write)),
        ]
        out[.health] = one("Score", WB.health, score)
        series = out
        var extra: [String: [MetricPoint]] = [:]
        for name in ["procs", "zombies", "pids", "files", "inodes", "mem_pressure", "psi_mem", "psi_io_full"] {
            extra[name] = pts(name, name == "procs" ? vm.procCount : nil)
        }
        named = extra
        latest = vm.cpu.latest?.t
        oldest = out[.cpu]?.first?.points.first?.t
    }

    @MainActor
    init(vm: ServerViewModel, window: ClosedRange<Date>, older: [HistoryPoint]) {
        self.window = window
        func join(_ live: MetricSeries, _ pick: (HistoryPoint) -> Double?) -> [MetricPoint] {
            let live = live.snapshot()
            let before = live.first.map { f in older.filter { $0.t < f.t } } ?? older
            let extra = before.compactMap { p in pick(p).map { MetricPoint(t: p.t, v: $0) } }
            return (extra + live).filter { window.contains($0.t) }
        }
        func one(_ name: String, _ color: Color, _ points: [MetricPoint]) -> [ChartSeries] {
            [ChartSeries(name: name, color: color, points: points)]
        }
        func clip(_ s: MetricSeries) -> [MetricPoint] {
            s.snapshot().filter { window.contains($0.t) }
        }
        var out: [RailMetric: [ChartSeries]] = [:]
        out[.cpu] = one("CPU", WB.cpu, join(vm.cpu, \.cpu))
        out[.mem] = one("Memory", WB.mem, join(vm.mem, \.mem))
        out[.load] = one("Load", WB.load, join(vm.load, \.load))
        out[.disk] = one("Disk", WB.disk, join(vm.disk, \.disk))
        out[.net] = [
            ChartSeries(name: "In", color: WB.netIn, points: join(vm.netRx, \.rxMBps)),
            ChartSeries(name: "Out", color: WB.netOut, points: join(vm.netTx, \.txMBps)),
        ]
        out[.io] = [
            ChartSeries(name: "Read", color: WB.read, points: clip(vm.diskIO.total.read)),
            ChartSeries(name: "Write", color: WB.write, points: clip(vm.diskIO.total.write)),
        ]
        out[.health] = one("Score", WB.health, join(vm.healthScoreSeries, \.score))
        series = out
        named = ["procs": clip(vm.procCount)]
        latest = vm.cpu.latest?.t
        oldest = ([vm.cpu.snapshot().first?.t, older.first?.t].compactMap { $0 }).min()
    }

    private init(window: ClosedRange<Date>, series: [RailMetric: [ChartSeries]], latest: Date?, oldest: Date?) {
        self.window = window
        self.series = series
        self.latest = latest
        self.oldest = oldest
    }

    func points(_ m: RailMetric, _ i: Int = 0) -> [MetricPoint] {
        guard let s = series[m], i < s.count else { return [] }
        return s[i].points
    }

    /// The value of series `i` at time `t`: the nearest point, within the window.
    func value(_ m: RailMetric, _ i: Int = 0, at t: Date?) -> Double? {
        let pts = points(m, i)
        guard let t else { return pts.last?.v }
        return MetricChart.nearest(in: pts, to: t)?.v
    }
}
