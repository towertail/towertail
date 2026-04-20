import Foundation

struct MockCollector: Collector {
    static let tickInterval: TimeInterval = 15
    static let backfillSamples: Int = 480

    func run(sink: ServerStore) async {
        let hosts = await Self.defaultHosts()

        await MainActor.run {
            for h in hosts { sink.register(h.vm) }
        }

        let now = Date()
        let start = now.addingTimeInterval(-Self.tickInterval * Double(Self.backfillSamples))
        for i in 0..<Self.backfillSamples {
            let t = start.addingTimeInterval(Self.tickInterval * Double(i))
            for h in hosts {
                if let sample = h.synth.sample(at: t) {
                    let id = h.vm.id
                    await MainActor.run {
                        sink.ingest(sample, for: id)
                    }
                } else if let reason = h.synth.offlineReason {
                    let offlineAt = h.synth.lastSeenAt ?? t
                    let id = h.vm.id
                    await MainActor.run {
                        sink.markOffline(id: id, reason: reason, at: offlineAt)
                    }
                }
            }
        }

        while !Task.isCancelled {
            try? await Task.sleep(nanoseconds: UInt64(Self.tickInterval * 1_000_000_000))
            if Task.isCancelled { break }
            let t = Date()
            for h in hosts {
                if let sample = h.synth.sample(at: t) {
                    let id = h.vm.id
                    await MainActor.run {
                        sink.ingest(sample, for: id)
                    }
                }
            }
        }
    }

    @MainActor
    static func defaultHosts() -> [MockHost] {
        [
            MockHost(
                vm: ServerViewModel(
                    hostname: "web-edge-1",
                    dnsName: "web-edge-1.tailnet.ts.net",
                    osArch: "linux/arm64"
                ),
                synth: SyntheticHost(profile: .nominal, name: "web-edge-1", os: "linux", arch: "arm64")
            ),
            MockHost(
                vm: ServerViewModel(
                    hostname: "db-primary",
                    dnsName: "db-primary.tailnet.ts.net",
                    osArch: "linux/x86_64"
                ),
                synth: SyntheticHost(profile: .warn, name: "db-primary", os: "linux", arch: "x86_64")
            ),
            MockHost(
                vm: ServerViewModel(
                    hostname: "queue-worker-2",
                    dnsName: "queue-worker-2.tailnet.ts.net",
                    osArch: "linux/arm64"
                ),
                synth: SyntheticHost(profile: .critical, name: "queue-worker-2", os: "linux", arch: "arm64")
            ),
            MockHost(
                vm: ServerViewModel(
                    hostname: "backup-node",
                    dnsName: "backup-node.tailnet.ts.net",
                    osArch: "linux/x86_64",
                    state: .offline(reason: "ssh: connection refused")
                ),
                synth: SyntheticHost(profile: .offline, name: "backup-node", os: "linux", arch: "x86_64")
            ),
        ]
    }
}

struct MockHost {
    let vm: ServerViewModel
    let synth: SyntheticHost
}

enum HostProfile: Sendable {
    case nominal
    case warn
    case critical
    case offline
}

final class SyntheticHost: @unchecked Sendable {
    let profile: HostProfile
    let name: String
    let os: String
    let arch: String
    private var rng: LCG
    private var tickIndex: Int = 0
    private var netRxCum: Int64 = 0
    private var netTxCum: Int64 = 0
    let offlineCutoff: Date = Date()
    var lastSeenAt: Date?

    init(profile: HostProfile, name: String, os: String, arch: String) {
        self.profile = profile
        self.name = name
        self.os = os
        self.arch = arch
        let seed: UInt64 = name.reduce(1469598103934665603) { ($0 ^ UInt64($1.asciiValue ?? 0)) &* 1099511628211 }
        self.rng = LCG(seed: seed)
    }

    var offlineReason: String? {
        profile == .offline ? "ssh: connection refused" : nil
    }

    func sample(at t: Date) -> Sample? {
        defer { tickIndex += 1 }
        if profile == .offline {
            if lastSeenAt == nil {
                lastSeenAt = t.addingTimeInterval(-4 * 60)
            }
            return nil
        }
        lastSeenAt = t

        let ti = Double(tickIndex)
        let noise = { (amp: Double) -> Double in (self.rng.nextUnit() - 0.5) * 2.0 * amp }

        let cpuPct: Double
        let memPct: Double
        let diskPct: Double
        let rxBps: Int64
        let txBps: Int64

        switch profile {
        case .nominal:
            cpuPct = clamp(35 + 10 * sin(ti / 90.0) + noise(4), 0, 100)
            memPct = clamp(50 + 5 * sin(ti / 600.0), 0, 100)
            diskPct = 37
            let rxMB = 3.2 + noise(0.5) + (tickIndex % 18 == 0 ? 8.0 : 0.0)
            let txMB = 0.8 + noise(0.2)
            rxBps = Int64(max(0, rxMB) * 1_048_576)
            txBps = Int64(max(0, txMB) * 1_048_576)
        case .warn:
            cpuPct = clamp(35 + 10 * sin(ti / 90.0) + noise(4), 0, 100)
            let ramp = Double(tickIndex % 480) / 480.0
            memPct = clamp(65 + 20 * ramp, 0, 100)
            diskPct = 37
            let rxMB = 3.2 + noise(0.5)
            let txMB = 0.8 + noise(0.2)
            rxBps = Int64(max(0, rxMB) * 1_048_576)
            txBps = Int64(max(0, txMB) * 1_048_576)
        case .critical:
            cpuPct = clamp(70 + 8 * sin(ti / 60.0) + noise(3), 0, 100)
            memPct = clamp(50 + noise(3), 0, 100)
            let dCycle = Double(tickIndex % 40) / 40.0
            diskPct = clamp(82 + dCycle * 16, 0, 100)
            let rxMB = 28 + noise(4)
            let txMB = 10 + noise(2)
            rxBps = Int64(max(0, rxMB) * 1_048_576)
            txBps = Int64(max(0, txMB) * 1_048_576)
        case .offline:
            return nil
        }

        netRxCum &+= Int64(Double(rxBps) * MockCollector.tickInterval)
        netTxCum &+= Int64(Double(txBps) * MockCollector.tickInterval)

        let totalMem: Int64 = 16 * 1024 * 1024 * 1024
        let usedMem = Int64(Double(totalMem) * memPct / 100.0)
        let totalDisk: Int64 = 100 * 1024 * 1024 * 1024
        let usedDisk = Int64(Double(totalDisk) * diskPct / 100.0)

        return Sample(
            v: 1,
            ts: t,
            host: HostInfo(
                name: name, os: os, arch: arch,
                kernel: os == "linux" ? "6.6.22-1-\(arch)" : "24.0.0",
                uptimeS: 1048273, agent: "0.1.0+mock"
            ),
            cpu: CPUInfo(pct: cpuPct, load1: cpuPct / 100 * 2, load5: cpuPct / 100 * 1.8, load15: cpuPct / 100 * 1.5, cores: 8),
            mem: MemInfo(used: usedMem, total: totalMem),
            swap: MemInfo(used: 0, total: 0),
            disks: [DiskSample(mount: "/", fs: "ext4", used: usedDisk, total: totalDisk)],
            net: NetInfo(rxBps: rxBps, txBps: txBps, rxCum: netRxCum, txCum: netTxCum),
            errors: []
        )
    }
}

struct LCG {
    private var state: UInt64
    init(seed: UInt64) { state = seed == 0 ? 0xdeadbeef : seed }
    mutating func next() -> UInt64 {
        state = state &* 6364136223846793005 &+ 1442695040888963407
        return state
    }
    mutating func nextUnit() -> Double {
        Double(next() >> 11) / Double(1 << 53)
    }
}

private func clamp(_ v: Double, _ lo: Double, _ hi: Double) -> Double {
    min(max(v, lo), hi)
}
