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

    var netRxMBps: Double = 0
    var netTxMBps: Double = 0

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
    }

    @discardableResult
    func ingest(_ s: Sample) -> HistoryPoint {
        lastSeen = s.ts
        let cpuFrac = computeCPUFraction(from: s.cpu)
        let cpuV = min(max(cpuFrac, 0), 1)
        cpu.append(MetricPoint(t: s.ts, v: cpuV))

        let memFrac = s.mem.total > 0 ? Double(s.mem.used) / Double(s.mem.total) : 0
        let memV = min(max(memFrac, 0), 1)
        mem.append(MetricPoint(t: s.ts, v: memV))

        let worstDisk: Double = {
            guard let disks = s.disks, !disks.isEmpty else { return 0 }
            return disks.reduce(0.0) { acc, d in
                let f = d.total > 0 ? Double(d.used) / Double(d.total) : 0
                return max(acc, f)
            }
        }()
        let diskV = min(max(worstDisk, 0), 1)
        disk.append(MetricPoint(t: s.ts, v: diskV))

        var netV: Double? = nil
        if let n = s.net {
            let (rxBps, txBps) = computeNetRates(rxCum: n.rxCum, txCum: n.txCum, ts: s.ts, fallbackRx: n.rxBps, fallbackTx: n.txBps)
            netRxMBps = Double(rxBps) / 1_048_576.0
            netTxMBps = Double(txBps) / 1_048_576.0
            let normalized = min(1.0, (netRxMBps + netTxMBps) / 100.0)
            netV = normalized
            net.append(MetricPoint(t: s.ts, v: normalized))
        }

        state = computeState()
        return HistoryPoint(
            t: s.ts,
            cpu: cpuV, mem: memV, disk: diskV, net: netV,
            rxMBps: s.net.map { Double($0.rxBps) / 1_048_576.0 },
            txMBps: s.net.map { Double($0.txBps) / 1_048_576.0 }
        )
    }

    /// Replay persisted history from a prior session. Called on VM creation;
    /// safe to call before any live samples arrive.
    func hydrate(from points: [HistoryPoint]) {
        for p in points {
            if let v = p.cpu { cpu.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.mem { mem.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.disk { disk.append(MetricPoint(t: p.t, v: v)) }
            if let v = p.net { net.append(MetricPoint(t: p.t, v: v)) }
        }
        if let last = points.last {
            lastSeen = last.t
            if let rx = last.rxMBps { netRxMBps = rx }
            if let tx = last.txMBps { netTxMBps = tx }
        }
    }

    func markOffline(reason: String, at t: Date) {
        state = .offline(reason: reason)
        lastSeen = t
    }

    /// Prefer counter-delta math when the agent supplies cumulative totals;
    /// fall back to the agent's short-window `pct` when counters are absent
    /// (first tick after launch, or agents that don't emit them).
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

    /// Prefer counter-delta math for network rates. The agent's in-process
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

    var statusDotColor: Color {
        worstTint.color
    }
}

struct MetricThresholds: Sendable, Equatable {
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
