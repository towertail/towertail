import Foundation
import SwiftUI

enum ServerConnState: Equatable, Sendable {
    case unknown
    case online
    case warn
    case critical
    case offline(reason: String)
    /// Polling is deliberately paused on the Mac side — we don't know if
    /// the host is up or down because we haven't tried. Distinct from
    /// `.offline` so the UI doesn't flag a server as DOWN when really
    /// our Mac is asleep or the local Wi‑Fi dropped.
    case suspended(reason: String)

    var isOffline: Bool {
        if case .offline = self { return true }
        return false
    }

    var isSuspended: Bool {
        if case .suspended = self { return true }
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
    /// True after the first successful sample has ever been ingested for
    /// this host. Drives the menu-bar escalation: a known-good host that
    /// is now offline is "critical" (red icon), whereas a brand-new host
    /// that has never connected stays just plain offline. Seeded from
    /// `Node.lastSuccessfulConnect` on startup so a relaunch keeps the
    /// memory.
    var everConnected: Bool = false

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

    /// Latest-known per-process ports snapshot. Refreshed every
    /// `--ports-interval` (default 10s) on the sampler side and re-emitted
    /// unchanged in between, so the value here changes infrequently.
    /// Snapshot-only (not time-windowed) — the table just shows the
    /// freshest known state with `collected_ts` rendered as staleness.
    var ports: PortList?
    /// True when at least one sample carried a `ports` payload. Used to
    /// drive the "ports collection disabled" state separately from procs
    /// since `--no-ports` and `--no-proc` are independent flags.
    var portsAvailable: Bool = false

    var hoverDate: Date?

    var thresholds: MetricThresholds

    private var prevCPUTotalMs: Int64?
    private var prevCPUBusyMs: Int64?
    private var prevNetRxCum: Int64?
    private var prevNetTxCum: Int64?
    private var prevNetTS: Date?

    /// Consecutive over-warn / over-critical sample counts per metric. Reset
    /// to zero on the first sample that's *under* the corresponding threshold.
    /// Drives the per-metric sustain gate; lives in memory only — restarts
    /// start fresh, which matches the user's mental model (a freshly relaunched
    /// app shouldn't fire on a backlog of historical spikes).
    @ObservationIgnored private var warnStreak: [Metric: Int] = [:]
    @ObservationIgnored private var criticalStreak: [Metric: Int] = [:]

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

        if let pl = s.ports {
            ports = pl
            portsAvailable = true
        }

        updateStreak(.cpu, value: cpuV, warn: thresholds.cpuWarn, critical: thresholds.cpuCritical)
        updateStreak(.mem, value: memV, warn: thresholds.memWarn, critical: thresholds.memCritical)
        updateStreak(.disk, value: diskV, warn: thresholds.diskWarn, critical: thresholds.diskCritical)

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

    /// Replay persisted per-mount capacity rows. Driven by `ServerStore`
    /// at register time so the DISK tab's capacity chart shows a full
    /// 2h window on launch instead of starting empty.
    func hydrateDiskCapacity(from rows: [HistoryStore.DiskCapacityRow]) {
        disksPerMount.hydrate(rows: rows)
    }

    /// Replay persisted per-device I/O rows. Same rationale as
    /// `hydrateDiskCapacity` — the DISK tab's I/O chart needs 2h of
    /// history across an app restart, not a fresh-start 0.
    func hydrateDiskIO(from rows: [HistoryStore.DiskIORow]) {
        diskIO.hydrate(rows: rows)
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

    /// Park this VM in a "we can't reach this host because *our* Mac can't
    /// try right now" state — sleep, no internet, etc. Does not touch
    /// `lastSeen` so the stopwatch in the header continues from the last
    /// real sample, which is the truthful thing to show.
    func markSuspended(reason: String) {
        let previous = state
        // Don't overwrite an existing suspended state with a re-entry
        // (e.g. network flap during sleep); only log real transitions.
        if case .suspended(let r) = previous, r == reason { return }
        state = .suspended(reason: reason)
        if case .suspended = previous { return }
        Logger.shared.info(
            "state: \(Self.stateLabel(previous)) → suspended",
            category: "thresholds",
            hostID: id, host: hostname,
            kv: ["reason": reason]
        )
    }

    /// Clear a suspended state back to `unknown`. The next successful
    /// ingest will push it into `online`/`warn`/`critical`; a failure
    /// will push it into `offline`. Leaves `lastSeen` alone.
    func clearSuspended() {
        guard case .suspended = state else { return }
        let previous = state
        state = .unknown
        Logger.shared.info(
            "state: \(Self.stateLabel(previous)) → unknown",
            category: "thresholds",
            hostID: id, host: hostname
        )
    }

    private static func stateLabel(_ s: ServerConnState) -> String {
        switch s {
        case .unknown: return "unknown"
        case .online: return "online"
        case .warn: return "warn"
        case .critical: return "critical"
        case .offline(let reason): return "offline(\(reason))"
        case .suspended(let reason): return "suspended(\(reason))"
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
            sustainedTint(for: .cpu, raw: cpu.tint(warn: thresholds.cpuWarn, critical: thresholds.cpuCritical)),
            sustainedTint(for: .mem, raw: mem.tint(warn: thresholds.memWarn, critical: thresholds.memCritical)),
            sustainedTint(for: .disk, raw: disk.tint(warn: thresholds.diskWarn, critical: thresholds.diskCritical)),
        ]
        if tints.contains(.critical) { return .critical }
        if tints.contains(.warn) { return .warn }
        return .online
    }

    /// Increments or resets per-metric warn/critical streaks based on the
    /// latest value. A single under-threshold sample resets the streak so a
    /// host that drops back to nominal stops being flagged immediately.
    private func updateStreak(_ metric: Metric, value: Double, warn: Double, critical: Double) {
        if value >= warn {
            warnStreak[metric, default: 0] += 1
        } else {
            warnStreak[metric] = 0
        }
        if value >= critical {
            criticalStreak[metric, default: 0] += 1
        } else {
            criticalStreak[metric] = 0
        }
    }

    /// Maps a raw tint to its sustain-gated equivalent. If the streak hasn't
    /// reached the configured `sustainSamples`, the tint is downgraded —
    /// critical → warn → nominal. With the default of 1, this is a no-op.
    private func sustainedTint(for metric: Metric, raw: ThresholdTint) -> ThresholdTint {
        let need = thresholds.sustainSamples(for: metric)
        if need <= 1 { return raw }
        switch raw {
        case .critical:
            if (criticalStreak[metric] ?? 0) >= need { return .critical }
            if (warnStreak[metric] ?? 0) >= need { return .warn }
            return .nominal
        case .warn:
            if (warnStreak[metric] ?? 0) >= need { return .warn }
            return .nominal
        case .nominal, .stale:
            return raw
        }
    }

    var worstTint: ThresholdTint {
        switch state {
        case .offline:
            // A previously-good host that's now offline is a real
            // incident — surface it red on the card too, matching the
            // menu-bar escalation. Brand-new hosts that have never
            // connected stay neutral to avoid screaming about a setup
            // the user is still working on.
            return everConnected ? .critical : .stale
        case .suspended: return .stale
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
        case .cpu: return sustainedTint(for: .cpu, raw: cpu.tint(warn: thresholds.cpuWarn, critical: thresholds.cpuCritical))
        case .mem: return sustainedTint(for: .mem, raw: mem.tint(warn: thresholds.memWarn, critical: thresholds.memCritical))
        case .disk: return sustainedTint(for: .disk, raw: disk.tint(warn: thresholds.diskWarn, critical: thresholds.diskCritical))
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
    /// Minimum number of consecutive over-threshold samples required before
    /// a metric is allowed to escalate the host's state or fire a notification.
    /// 1 (default) preserves the original "fire on first crossing" behavior.
    /// Shared between warn and critical (per-metric, not per-severity); the
    /// clear path is unconditional — a single under-threshold sample resets
    /// the streak so a flapping host stops being warn/critical instantly.
    var cpuSustainSamples: Int
    var memSustainSamples: Int
    var diskSustainSamples: Int

    static let defaults = MetricThresholds(
        cpuWarn: 0.75, cpuCritical: 0.90,
        memWarn: 0.75, memCritical: 0.90,
        diskWarn: 0.85, diskCritical: 0.95,
        cpuSustainSamples: 1,
        memSustainSamples: 1,
        diskSustainSamples: 1
    )

    enum CodingKeys: String, CodingKey {
        case cpuWarn, cpuCritical, memWarn, memCritical, diskWarn, diskCritical
        case cpuSustainSamples, memSustainSamples, diskSustainSamples
    }

    init(
        cpuWarn: Double, cpuCritical: Double,
        memWarn: Double, memCritical: Double,
        diskWarn: Double, diskCritical: Double,
        cpuSustainSamples: Int = 1,
        memSustainSamples: Int = 1,
        diskSustainSamples: Int = 1
    ) {
        self.cpuWarn = cpuWarn
        self.cpuCritical = cpuCritical
        self.memWarn = memWarn
        self.memCritical = memCritical
        self.diskWarn = diskWarn
        self.diskCritical = diskCritical
        self.cpuSustainSamples = max(1, cpuSustainSamples)
        self.memSustainSamples = max(1, memSustainSamples)
        self.diskSustainSamples = max(1, diskSustainSamples)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let cpuW = try c.decode(Double.self, forKey: .cpuWarn)
        let cpuC = try c.decode(Double.self, forKey: .cpuCritical)
        let memW = try c.decode(Double.self, forKey: .memWarn)
        let memC = try c.decode(Double.self, forKey: .memCritical)
        let diskW = try c.decode(Double.self, forKey: .diskWarn)
        let diskC = try c.decode(Double.self, forKey: .diskCritical)
        let cpuS = try c.decodeIfPresent(Int.self, forKey: .cpuSustainSamples) ?? 1
        let memS = try c.decodeIfPresent(Int.self, forKey: .memSustainSamples) ?? 1
        let diskS = try c.decodeIfPresent(Int.self, forKey: .diskSustainSamples) ?? 1
        self.init(
            cpuWarn: cpuW, cpuCritical: cpuC,
            memWarn: memW, memCritical: memC,
            diskWarn: diskW, diskCritical: diskC,
            cpuSustainSamples: cpuS,
            memSustainSamples: memS,
            diskSustainSamples: diskS
        )
    }

    func sustainSamples(for metric: Metric) -> Int {
        switch metric {
        case .cpu: return cpuSustainSamples
        case .mem: return memSustainSamples
        case .disk: return diskSustainSamples
        case .net: return 1
        }
    }

    /// Effective thresholds for a host: warn/critical levels come from the
    /// per-node override (when set), but `*SustainSamples` always inherits
    /// from the global. Sustain is a global noise filter — there is no
    /// per-node UI for it, so any stale value in `customThresholds` from
    /// before the sustain stepper existed (or from a parallel client) would
    /// silently override the user's global setting otherwise.
    static func effective(global: MetricThresholds, override: MetricThresholds?) -> MetricThresholds {
        guard let override else { return global }
        return MetricThresholds(
            cpuWarn: override.cpuWarn, cpuCritical: override.cpuCritical,
            memWarn: override.memWarn, memCritical: override.memCritical,
            diskWarn: override.diskWarn, diskCritical: override.diskCritical,
            cpuSustainSamples: global.cpuSustainSamples,
            memSustainSamples: global.memSustainSamples,
            diskSustainSamples: global.diskSustainSamples
        )
    }
}
