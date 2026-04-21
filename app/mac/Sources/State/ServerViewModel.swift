import Foundation
import SwiftUI

enum ServerConnState: Equatable, Sendable {
    case unknown
    case online
    case warn
    case critical
    case offline(reason: String)

    var isOffline: Bool {
        if case .offline = self { return true }
        return false
    }
}

@Observable
@MainActor
final class ServerViewModel: Identifiable {
    let id: UUID
    var hostname: String
    var dnsName: String
    var osArch: String
    var kind: NodeKind
    var state: ServerConnState
    var lastSeen: Date?

    var cpu: MetricSeries
    var mem: MetricSeries
    var disk: MetricSeries
    var net: MetricSeries
    /// Per-sample rx/tx in MB/s so hover can reconstruct the historical rate.
    var netRx: MetricSeries
    var netTx: MetricSeries
    /// Per-mount capacity history. `disk` (above) is the max across
    /// mounts — kept as a denormalized cache so existing call sites
    /// don't churn. `disksPerMount.max` is the same data.
    var disksPerMount: DiskSeries
    /// Per-device disk I/O rate (MB/s) history.
    var diskIO: DiskIOSeries

    var netRxMBps: Double = 0
    var netTxMBps: Double = 0

    /// Sampler version+sha string reported in the most recent sample's
    /// `host.sampler` field. Empty until the first successful sample
    /// arrives. Surfaced in the Servers table so users can see which
    /// hosts are on the current binary and which still need an update.
    var samplerVersion: String = ""

    var procs: ProcSeries
    /// True when at least one ingested sample included a procs payload —
    /// lets the UI show a clear "process collection disabled" state for
    /// samplers invoked with `--no-proc` rather than a flicker of empty.
    var procsAvailable: Bool = false
    /// Latest-known root status of the sampler binary on the remote host.
    /// Drives the "root" vs "user scope" badge in the process table.
    var procsRoot: Bool = false
    /// Set to true the first time `ServerStore.ensureProcsHydrated` is
    /// asked to load this host's proc history from SQLite. Used to
    /// debounce repeated hydration kickoffs when the user opens and
    /// closes the full view multiple times in a session.
    @ObservationIgnored
    var procsHydrationStarted: Bool = false

    var hoverDate: Date?

    var thresholds: MetricThresholds

    private var prevCPUTotalMs: Int64?
    private var prevCPUBusyMs: Int64?
    private var prevNetRxCum: Int64?
    private var prevNetTxCum: Int64?
    private var prevNetTS: Date?

    init(
        id: UUID = UUID(),
        hostname: String,
        dnsName: String,
        osArch: String,
        kind: NodeKind = .local,
        state: ServerConnState = .unknown,
        thresholds: MetricThresholds = .defaults
    ) {
        self.id = id
        self.hostname = hostname
        self.dnsName = dnsName
        self.osArch = osArch
        self.kind = kind
        self.state = state
        self.thresholds = thresholds
        self.cpu = MetricSeries()
        self.mem = MetricSeries()
        self.disk = MetricSeries()
        self.net = MetricSeries()
        self.netRx = MetricSeries()
        self.netTx = MetricSeries()
        self.disksPerMount = DiskSeries()
        self.diskIO = DiskIOSeries()
        self.procs = ProcSeries()
    }

    @discardableResult
    func ingest(_ s: Sample) -> HistoryPoint {
        lastSeen = s.ts
        // Pick up the OS/arch the sampler reported. Without this, remote
        // hosts stay stuck on their placeholder "—" because osArch is
        // only set at VM creation time.
        let os = Self.prettyOSName(s.host.os)
        if !os.isEmpty {
            osArch = s.host.arch.isEmpty ? os : "\(os) · \(s.host.arch)"
        }
        if !s.host.sampler.isEmpty {
            samplerVersion = s.host.sampler
        }
        let cpuFrac = computeCPUFraction(from: s.cpu)
        let cpuV = min(max(cpuFrac, 0), 1)
        cpu.append(MetricPoint(t: s.ts, v: cpuV))

        let memFrac = s.mem.total > 0 ? Double(s.mem.used) / Double(s.mem.total) : 0
        let memV = min(max(memFrac, 0), 1)
        mem.append(MetricPoint(t: s.ts, v: memV))

        if let disks = s.disks, !disks.isEmpty {
            disksPerMount.append(disks, at: s.ts)
        }
        // The scalar "worst mount" stream — fed by the DiskSeries.max
        // series so the denormalized `disk` matches the per-mount data
        // exactly (no rounding drift). Fall back to the previous reduce
        // when there are no mounts in this sample so the hydrate path
        // and samples without `disks` still produce a point.
        let diskV: Double = {
            if let latest = disksPerMount.max.latest, latest.t == s.ts {
                return latest.v
            }
            guard let disks = s.disks, !disks.isEmpty else { return 0 }
            return disks.reduce(0.0) { acc, d in
                let f = d.total > 0 ? Double(d.used) / Double(d.total) : 0
                return Swift.max(acc, f)
            }
        }()
        disk.append(MetricPoint(t: s.ts, v: min(max(diskV, 0), 1)))

        if let io = s.diskIO {
            diskIO.append(
                at: s.ts,
                totalReadBps: io.readBps,
                totalWriteBps: io.writeBps,
                devices: io.devices
            )
        }

        var netV: Double? = nil
        var rxV: Double? = nil
        var txV: Double? = nil
        if let n = s.net {
            let (rxBps, txBps) = computeNetRates(rxCum: n.rxCum, txCum: n.txCum, ts: s.ts, fallbackRx: n.rxBps, fallbackTx: n.txBps)
            netRxMBps = Double(rxBps) / 1_048_576.0
            netTxMBps = Double(txBps) / 1_048_576.0
            let normalized = min(1.0, (netRxMBps + netTxMBps) / 100.0)
            netV = normalized
            rxV = netRxMBps
            txV = netTxMBps
            net.append(MetricPoint(t: s.ts, v: normalized))
            netRx.append(MetricPoint(t: s.ts, v: netRxMBps))
            netTx.append(MetricPoint(t: s.ts, v: netTxMBps))
        }

        if let ps = s.procs {
            procs.append(ProcSeries.Snapshot(t: s.ts, items: ps.items))
            procsAvailable = true
            procsRoot = ps.root
        }

        let previous = state
        state = computeState()
        if previous != state {
            Logger.shared.info(
                "state: \(Self.stateLabel(previous)) → \(Self.stateLabel(state))",
                category: "thresholds",
                hostID: id, host: hostname,
                kv: [
                    "cpu_pct": String(format: "%.0f", cpuV * 100),
                    "mem_pct": String(format: "%.0f", memV * 100),
                    "disk_pct": String(format: "%.0f", diskV * 100),
                ]
            )
        }
        return HistoryPoint(
            t: s.ts,
            cpu: cpuV, mem: memV, disk: diskV, net: netV,
            rxMBps: rxV,
            txMBps: txV
        )
    }

    /// Replay persisted process snapshots from the prior session. Called
    /// on VM creation; the caller loads from SQLite and passes them in
    /// oldest-first, matching the append order `ProcSeries` expects.
    /// Also restores the "root sampler" / "procs available" flags from
    /// the most recent row so the UI doesn't flicker through the
    /// "disabled" state on cold start.
    func hydrateProcs(from rows: [HistoryStore.ProcHistoryRow]) {
        let snapshots = rows.map { ProcSeries.Snapshot(t: $0.t, items: $0.items) }
        procs.replace(with: snapshots)
        if let last = rows.last {
            procsAvailable = true
            procsRoot = last.root
        }
    }

    /// Replay persisted history from a prior session. Called on VM creation;
    /// safe to call before any live samples arrive.
    func hydrate(from points: [HistoryPoint]) {
        for p in points {
            if let v = p.cpu { cpu.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.mem { mem.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.disk { disk.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.net { net.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.rxMBps { netRx.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.txMBps { netTx.append(MetricPoint(t: p.t, v: v)) }
        }
        if let last = points.last {
            lastSeen = last.t
            if let rx = last.rxMBps { netRxMBps = rx }
            if let tx = last.txMBps { netTxMBps = tx }
        }
    }

    func markOffline(reason: String, at t: Date) {
        let previous = state
        state = .offline(reason: reason)
        lastSeen = t
        if case .offline = previous { return }
        Logger.shared.warn(
            "state: \(Self.stateLabel(previous)) → offline",
            category: "thresholds",
            hostID: id, host: hostname,
            kv: ["reason": reason]
        )
    }

    private static func stateLabel(_ s: ServerConnState) -> String {
        switch s {
        case .unknown: return "unknown"
        case .online: return "online"
        case .warn: return "warn"
        case .critical: return "critical"
        case .offline(let reason): return "offline(\(reason))"
        }
    }

    /// Map the raw `runtime.GOOS`-style strings the sampler emits onto names
    /// a human would expect to see in the UI (darwin → macOS).
    private static func prettyOSName(_ raw: String) -> String {
        switch raw.lowercased() {
        case "": return ""
        case "darwin": return "macOS"
        case "linux": return "Linux"
        case "windows": return "Windows"
        case "freebsd": return "FreeBSD"
        default: return raw
        }
    }

    /// Prefer counter-delta math when the sampler supplies cumulative totals;
    /// fall back to the sampler's short-window `pct` when counters are absent
    /// (first tick after launch, or samplers that don't emit them).
    private func computeCPUFraction(from info: CPUInfo) -> Double {
        if let total = info.totalMs, let busy = info.busyMs, total > 0 {
            defer {
                prevCPUTotalMs = total
                prevCPUBusyMs = busy
            }
            if let prevTotal = prevCPUTotalMs, let prevBusy = prevCPUBusyMs {
                let dt = total - prevTotal
                let db = busy - prevBusy
                // Reboot / counter reset: dt < 0, or busy went backwards.
                if dt > 0 && db >= 0 {
                    return Double(db) / Double(dt)
                }
            }
        }
        return info.pct / 100.0
    }

    /// Prefer counter-delta math for network rates. The sampler's in-process
    /// `rx_bps`/`tx_bps` are sampled over a short window and massively
    /// undersample bursty traffic; cumulative counters delta'd against the
    /// previous tick give a true rate over the full poll interval.
    private func computeNetRates(
        rxCum: Int64, txCum: Int64, ts: Date,
        fallbackRx: Int64, fallbackTx: Int64
    ) -> (rxBps: Int64, txBps: Int64) {
        defer {
            prevNetRxCum = rxCum
            prevNetTxCum = txCum
            prevNetTS = ts
        }
        if let pRx = prevNetRxCum, let pTx = prevNetTxCum, let pTS = prevNetTS {
            let dt = ts.timeIntervalSince(pTS)
            let dRx = rxCum - pRx
            let dTx = txCum - pTx
            // Reboot / iface change / counter reset: fall back.
            if dt > 0 && dRx >= 0 && dTx >= 0 {
                return (Int64(Double(dRx) / dt), Int64(Double(dTx) / dt))
            }
        }
        return (max(fallbackRx, 0), max(fallbackTx, 0))
    }

    private func computeState() -> ServerConnState {
        let tints = [
            cpu.tint(warn: thresholds.cpuWarn, critical: thresholds.cpuCritical),
            mem.tint(warn: thresholds.memWarn, critical: thresholds.memCritical),
            disk.tint(warn: thresholds.diskWarn, critical: thresholds.diskCritical),
        ]
        if tints.contains(.critical) { return .critical }
        if tints.contains(.warn) { return .warn }
        return .online
    }

    var worstTint: ThresholdTint {
        switch state {
        case .offline: return .stale
        case .critical: return .critical
        case .warn: return .warn
        case .online: return .nominal
        case .unknown: return .stale
        }
    }

    /// Per-metric tint derived from the latest sample against current
    /// thresholds. Used by the notifier to attribute a transition to the
    /// specific metric that crossed, and by the UI when focusing a notif.
    func tint(for metric: Metric) -> ThresholdTint {
        switch metric {
        case .cpu: return cpu.tint(warn: thresholds.cpuWarn, critical: thresholds.cpuCritical)
        case .mem: return mem.tint(warn: thresholds.memWarn, critical: thresholds.memCritical)
        case .disk: return disk.tint(warn: thresholds.diskWarn, critical: thresholds.diskCritical)
        case .net: return .nominal
        }
    }

    var statusDotColor: Color {
        worstTint.color
    }
}

struct MetricThresholds: Sendable, Equatable, Codable {
    var cpuWarn: Double
    var cpuCritical: Double
    var memWarn: Double
    var memCritical: Double
    var diskWarn: Double
    var diskCritical: Double

    static let defaults = MetricThresholds(
        cpuWarn: 0.75, cpuCritical: 0.90,
        memWarn: 0.75, memCritical: 0.90,
        diskWarn: 0.85, diskCritical: 0.95
    )
}
