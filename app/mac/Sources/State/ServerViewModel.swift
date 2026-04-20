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

    init(
        id: UUID = UUID(),
        hostname: String,
        dnsName: String,
        osArch: String,
        state: ServerConnState = .unknown,
        thresholds: MetricThresholds = .defaults
    ) {
        self.id = id
        self.hostname = hostname
        self.dnsName = dnsName
        self.osArch = osArch
        self.state = state
        self.thresholds = thresholds
        self.cpu = MetricSeries()
        self.mem = MetricSeries()
        self.disk = MetricSeries()
        self.net = MetricSeries()
    }

    func ingest(_ s: Sample) {
        lastSeen = s.ts
        cpu.append(MetricPoint(t: s.ts, v: min(max(s.cpu.pct / 100.0, 0), 1)))

        let memFrac = s.mem.total > 0 ? Double(s.mem.used) / Double(s.mem.total) : 0
        mem.append(MetricPoint(t: s.ts, v: min(max(memFrac, 0), 1)))

        let worstDisk: Double = {
            guard let disks = s.disks, !disks.isEmpty else { return 0 }
            return disks.reduce(0.0) { acc, d in
                let f = d.total > 0 ? Double(d.used) / Double(d.total) : 0
                return max(acc, f)
            }
        }()
        disk.append(MetricPoint(t: s.ts, v: min(max(worstDisk, 0), 1)))

        if let n = s.net {
            netRxMBps = Double(n.rxBps) / 1_048_576.0
            netTxMBps = Double(n.txBps) / 1_048_576.0
            let normalized = min(1.0, (netRxMBps + netTxMBps) / 100.0)
            net.append(MetricPoint(t: s.ts, v: normalized))
        }

        state = computeState()
    }

    func markOffline(reason: String, at t: Date) {
        state = .offline(reason: reason)
        lastSeen = t
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
