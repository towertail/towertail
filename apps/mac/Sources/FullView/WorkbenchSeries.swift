import Foundation

/// The REST series of a Cloud host for the window of the workbench. They stay
/// out of the live series (720 points), so a 30 d window fits.
@MainActor
@Observable
final class WorkbenchSeries {
    /// Units of the live series: ratios for CPU, memory, disk, inodes, PID and
    /// file use; MB/s for the network and the disk I/O.
    private(set) var series: [String: [MetricPoint]] = [:]
    private(set) var disks: RemoteDiskHistory?
    private(set) var loading = false
    private(set) var error: String?
    /// The plan limit moved the start of the window.
    private(set) var clamped = false

    private struct WindowKey: Equatable {
        let host: UUID
        let from: Date
        let to: Date
        let step: Duration
    }

    @ObservationIgnored private var key: WindowKey?
    @ObservationIgnored private var loadedTabs: Set<HostTab> = []

    private static let railMetrics = ["cpu", "mem", "load", "disk", "net_rx", "net_tx", "disk_read", "disk_write"]
    private static let healthMetrics = ["procs", "zombies", "pids", "files", "inodes"]

    /// The names for one tab. The server takes 16 at most. Mac hosts have the
    /// pressure `mem_pressure`; Linux hosts have PSI.
    static func metrics(for tab: HostTab, linux: Bool) -> [String] {
        let pressure = linux ? ["psi_mem", "psi_mem_full", "psi_io_full"] : ["mem_pressure"]
        switch tab {
        case .overview: return railMetrics + healthMetrics + pressure
        case .health: return ["cpu", "mem", "disk"] + healthMetrics + pressure
        case .processes: return ["cpu"]
        case .disks: return ["disk", "inodes", "disk_read", "disk_write"]
        case .alerts, .settings: return []
        }
    }

    /// Loads the series of the tab. A tab loads once for each host and window.
    func load(
        host: UUID, tab: HostTab, window: ClosedRange<Date>, step: Duration,
        hostOS: String = "", loader: any CloudHistoryLoader
    ) async {
        let next = WindowKey(host: host, from: window.lowerBound, to: window.upperBound, step: step)
        if next != key {
            // The old points stay until the new ones arrive, but not for another host.
            if key?.host != host {
                series = [:]
                disks = nil
            }
            key = next
            loadedTabs = []
            clamped = false
        }
        guard !loadedTabs.contains(tab) else { return }
        let metrics = Self.metrics(for: tab, linux: hostOS == "linux")
        guard !metrics.isEmpty else {
            loadedTabs.insert(tab)
            return
        }
        loading = true
        error = nil
        do {
            async let history = loader.window(host: host, metrics: metrics, window: window, step: step)
            async let mounts: RemoteDiskHistory? = tab == .disks ? loader.disks(host: host, window: window) : nil
            let (h, d) = try await (history, mounts)
            guard !Task.isCancelled, key == next else { return }
            for (name, points) in h.series {
                series[name] = points.map { MetricPoint(t: $0.ts, v: Self.convert(name, $0.value)) }
            }
            if let d { disks = d }
            clamped = clamped || h.clamped == true
            loadedTabs.insert(tab)
        } catch {
            guard !Task.isCancelled, key == next else { return }
            self.error = CloudHistoryError.message(error)
        }
        loading = false
    }

    /// The REST points of a metric, then the live points after the last one.
    func points(_ metric: String, live: MetricSeries?) -> [MetricPoint] {
        let rest = series[metric] ?? []
        guard let live else { return rest }
        guard let last = rest.last else { return live.snapshot() }
        return rest + live.snapshot().filter { $0.t > last.t }
    }

    /// The points of one mount, as a ratio of the used space.
    func mountPoints(_ mount: String) -> [MetricPoint] {
        guard let m = disks?.mounts.first(where: { $0.mount == mount }) else { return [] }
        return m.points.map { MetricPoint(t: $0.ts, v: $0.total > 0 ? Double($0.used) / Double($0.total) : 0) }
    }

    /// The inode use of a mount at its newest point.
    func inodeRatio(_ mount: String) -> Double? {
        guard let p = disks?.mounts.first(where: { $0.mount == mount })?.points.last else { return nil }
        return HealthStatus.fraction(p.inodesUsed.map { Int64(clamping: $0) }, p.inodesTotal.map { Int64(clamping: $0) })
    }

    static func convert(_ metric: String, _ v: Double) -> Double {
        switch metric {
        case "cpu": return min(max(v / 100, 0), 1)
        case "net_rx", "net_tx", "disk_read", "disk_write": return max(v, 0) / 1_048_576
        default: return v
        }
    }

    /// The health score at each time of the health metrics. It is the worst
    /// check, as `HealthScore`. A muted check does not count.
    func scorePoints(thresholds t: MetricThresholds, muted: MutedSignals, linux: Bool) -> [MetricPoint] {
        var rows: [Date: [String: Double]] = [:]
        for name in ["cpu", "mem", "disk", "procs", "zombies", "pids", "files", "inodes",
                     "mem_pressure", "psi_mem", "psi_mem_full", "psi_io_full"] {
            for p in series[name] ?? [] { rows[p.t, default: [:]][name] = p.v }
        }
        return rows.keys.sorted().compactMap { time in
            let r = rows[time]!
            var scores: [Double] = []
            func add(_ name: String, _ pair: ThresholdPair, muted isMuted: Bool) {
                guard !isMuted, let v = r[name] else { return }
                scores.append(HealthScore.score(value: v, warn: pair.warn, critical: pair.critical))
            }
            add("cpu", t[.cpu], muted: muted.mutes(Metric.cpu))
            add("mem", t[.mem], muted: muted.mutes(Metric.mem))
            add("disk", t[.disk], muted: muted.mutes(Metric.disk))
            add("procs", t[.procs], muted: muted.mutes(HealthStatus.Signal.procs))
            add("zombies", t[.zombies], muted: muted.mutes(HealthStatus.Signal.zombies))
            add("pids", t[.pids], muted: muted.mutes(HealthStatus.Signal.pids))
            add("files", t[.files], muted: muted.mutes(HealthStatus.Signal.files))
            add("inodes", t[.inodes], muted: muted.mutes(HealthStatus.Signal.inodes))
            if linux {
                if let some = r["psi_mem"], !muted.mutes(HealthStatus.Signal.memPressure) {
                    scores.append(HealthScore.psiScore(some: some, full: r["psi_mem_full"] ?? 0, pair: t.health.memPressure))
                }
                add("psi_io_full", t.health.ioPressure, muted: muted.mutes(HealthStatus.Signal.ioPressure))
            } else {
                add("mem_pressure", t.health.macMemPressure, muted: muted.mutes(HealthStatus.Signal.memPressure))
            }
            return scores.min().map { MetricPoint(t: time, v: $0) }
        }
    }
}
