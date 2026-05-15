import XCTest
@testable import Towertail

@MainActor
final class ServerViewModelTests: XCTestCase {
    private func makeSample(
        cpuPct: Double,
        memFrac: Double,
        diskFrac: Double,
        rxBps: Int64 = 0,
        txBps: Int64 = 0,
        t: Date = Date()
    ) -> Sample {
        let totalMem: Int64 = 16_000_000_000
        let totalDisk: Int64 = 100_000_000_000
        return Sample(
            v: 1, ts: t,
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: cpuPct, load1: 0, load5: 0, load15: 0, cores: 4),
            mem: MemInfo(used: Int64(Double(totalMem) * memFrac), total: totalMem),
            swap: MemInfo(used: 0, total: 0),
            disks: [DiskSample(mount: "/", fs: "apfs", used: Int64(Double(totalDisk) * diskFrac), total: totalDisk)],
            net: NetInfo(rxBps: rxBps, txBps: txBps, rxCum: 0, txCum: 0),
            errors: []
        )
    }

    func testStateTransitionsWithThresholds() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "linux/arm64")

        vm.ingest(makeSample(cpuPct: 20, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.state, .online)

        vm.ingest(makeSample(cpuPct: 76, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.state, .warn)

        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.state, .critical)
    }

    func testWorstDiskTakesMax() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let total: Int64 = 1_000
        let s = Sample(
            v: 1, ts: Date(),
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "k", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 0, load1: 0, load5: 0, load15: 0, cores: 1),
            mem: MemInfo(used: 0, total: 1),
            swap: MemInfo(used: 0, total: 1),
            disks: [
                DiskSample(mount: "/", fs: "apfs", used: 300, total: total),
                DiskSample(mount: "/data", fs: "apfs", used: 950, total: total),
            ],
            net: nil,
            errors: []
        )
        vm.ingest(s)
        XCTAssertEqual(vm.disk.latest?.v ?? 0, 0.95, accuracy: 0.0001)
    }

    func testNetRatesInMBps() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let oneMB: Int64 = 1_048_576
        // First tick has no prev → falls back to the sampler-provided rx_bps/tx_bps.
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, rxBps: 2 * oneMB, txBps: oneMB))
        XCTAssertEqual(vm.netRxMBps, 2.0, accuracy: 0.0001)
        XCTAssertEqual(vm.netTxMBps, 1.0, accuracy: 0.0001)
    }

    func testNetDeltaFromCumulativeCounters() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let oneMB: Int64 = 1_048_576
        let t0 = Date()
        // Seed prev counters via a first tick. Sampler rx_bps/tx_bps are 0 now.
        let s1 = Sample(
            v: 1, ts: t0,
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "k", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 0, load1: 0, load5: 0, load15: 0, cores: 1),
            mem: MemInfo(used: 1, total: 2), swap: MemInfo(used: 0, total: 0),
            disks: nil,
            net: NetInfo(rxBps: 0, txBps: 0, rxCum: 10 * oneMB, txCum: 4 * oneMB),
            errors: []
        )
        vm.ingest(s1)
        // Second tick 2 seconds later: 6 MiB more in, 2 MiB more out → 3 MiB/s rx, 1 MiB/s tx.
        let s2 = Sample(
            v: 1, ts: t0.addingTimeInterval(2),
            host: s1.host,
            cpu: s1.cpu,
            mem: s1.mem, swap: s1.swap,
            disks: nil,
            net: NetInfo(rxBps: 0, txBps: 0, rxCum: 16 * oneMB, txCum: 6 * oneMB),
            errors: []
        )
        vm.ingest(s2)
        XCTAssertEqual(vm.netRxMBps, 3.0, accuracy: 0.01)
        XCTAssertEqual(vm.netTxMBps, 1.0, accuracy: 0.01)
    }

    func testNetCounterResetFallsBackToSamplerBps() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let oneMB: Int64 = 1_048_576
        let t0 = Date()
        let s1 = Sample(
            v: 1, ts: t0,
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "k", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 0, load1: 0, load5: 0, load15: 0, cores: 1),
            mem: MemInfo(used: 1, total: 2), swap: MemInfo(used: 0, total: 0),
            disks: nil,
            net: NetInfo(rxBps: 0, txBps: 0, rxCum: 100 * oneMB, txCum: 50 * oneMB),
            errors: []
        )
        vm.ingest(s1)
        // Reboot: counters regress.
        let s2 = Sample(
            v: 1, ts: t0.addingTimeInterval(1),
            host: s1.host, cpu: s1.cpu, mem: s1.mem, swap: s1.swap,
            disks: nil,
            net: NetInfo(rxBps: 2 * oneMB, txBps: oneMB, rxCum: oneMB, txCum: oneMB / 2),
            errors: []
        )
        vm.ingest(s2)
        XCTAssertEqual(vm.netRxMBps, 2.0, accuracy: 0.01)
        XCTAssertEqual(vm.netTxMBps, 1.0, accuracy: 0.01)
    }

    func testLastSeenUpdated() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t = Date(timeIntervalSince1970: 1_700_000_000)
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, t: t))
        XCTAssertEqual(vm.lastSeen, t)
    }

    func testCPUDeltaUsesCumulativeCountersWhenProvided() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t0 = Date()
        let s1 = Sample(
            v: 1, ts: t0,
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 99, load1: 0, load5: 0, load15: 0, cores: 4,
                         idleMs: 8_000, totalMs: 10_000),
            mem: MemInfo(used: 1, total: 2), swap: MemInfo(used: 0, total: 0),
            disks: nil, net: nil, errors: []
        )
        // First tick has no prev → falls back to pct (99% => 0.99).
        vm.ingest(s1)
        XCTAssertEqual(vm.cpu.latest?.v ?? 0, 0.99, accuracy: 0.001)

        // Second tick: total advanced 1000ms, busy advanced 300ms → 30%.
        let s2 = Sample(
            v: 1, ts: t0.addingTimeInterval(1),
            host: s1.host,
            cpu: CPUInfo(pct: 99, load1: 0, load5: 0, load15: 0, cores: 4,
                         idleMs: 8_700, totalMs: 11_000),
            mem: s1.mem, swap: s1.swap, disks: nil, net: nil, errors: []
        )
        vm.ingest(s2)
        XCTAssertEqual(vm.cpu.latest?.v ?? 0, 0.30, accuracy: 0.001)
    }

    func testCPUFallsBackToPctWhenCountersMissing() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.ingest(makeSample(cpuPct: 42, memFrac: 0.1, diskFrac: 0.1))
        XCTAssertEqual(vm.cpu.latest?.v ?? 0, 0.42, accuracy: 0.0001)
    }

    func testCPUCounterResetFallsBackToPct() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t0 = Date()
        let mkSample: (Int64, Int64, Double) -> Sample = { idle, total, pct in
            Sample(
                v: 1, ts: t0,
                host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
                cpu: CPUInfo(pct: pct, load1: 0, load5: 0, load15: 0, cores: 4, idleMs: idle, totalMs: total),
                mem: MemInfo(used: 1, total: 2), swap: MemInfo(used: 0, total: 0),
                disks: nil, net: nil, errors: []
            )
        }
        vm.ingest(mkSample(9_000, 10_000, 10))
        // Counters reset (smaller totals). Should use pct (50%) not negative delta.
        vm.ingest(mkSample(100, 200, 50))
        XCTAssertEqual(vm.cpu.latest?.v ?? 0, 0.50, accuracy: 0.0001)
    }

    func testSustainOneIsImmediate() {
        // Default sustain=1 preserves the original "fire on first crossing" behavior.
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.state, .critical)
        XCTAssertEqual(vm.tint(for: .cpu), .critical)
    }

    func testSustainGatesEscalation() {
        let t = MetricThresholds(
            cpuWarn: 0.75, cpuCritical: 0.90,
            memWarn: 0.75, memCritical: 0.90,
            diskWarn: 0.85, diskCritical: 0.95,
            cpuSustainSamples: 3
        )
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", thresholds: t)

        // First two over-critical samples are gated — host stays online.
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)
        XCTAssertEqual(vm.state, .online)

        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)
        XCTAssertEqual(vm.state, .online)

        // Third over-critical sample meets the streak — escalates.
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .critical)
        XCTAssertEqual(vm.state, .critical)
    }

    func testSustainSharesCounterBetweenWarnAndCritical() {
        // Per-metric, shared between warn and critical — two warn samples
        // followed by a critical should escalate immediately to critical
        // because the warn streak is already met.
        let t = MetricThresholds(
            cpuWarn: 0.75, cpuCritical: 0.90,
            memWarn: 0.75, memCritical: 0.90,
            diskWarn: 0.85, diskCritical: 0.95,
            cpuSustainSamples: 2
        )
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", thresholds: t)

        vm.ingest(makeSample(cpuPct: 80, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)

        vm.ingest(makeSample(cpuPct: 80, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .warn)

        // Now a critical-level sample. Critical streak = 1 (< 2), but warn
        // streak = 3 (>= 2), so the gated tint downgrades to warn — the
        // host doesn't escalate to critical until the critical streak holds.
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .warn)

        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .critical)
    }

    func testSustainClearsImmediatelyOnDip() {
        // A single under-threshold sample resets the streak — no recovery
        // hold. Lets the user see the host return to nominal as soon as
        // the metric stops crossing the line.
        let t = MetricThresholds(
            cpuWarn: 0.75, cpuCritical: 0.90,
            memWarn: 0.75, memCritical: 0.90,
            diskWarn: 0.85, diskCritical: 0.95,
            cpuSustainSamples: 3
        )
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", thresholds: t)
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .critical)

        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)
        XCTAssertEqual(vm.state, .online)

        // After the dip, a fresh single spike does NOT escalate — must
        // re-earn the streak from scratch.
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.3, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)
    }

    func testSustainIsPerMetric() {
        // Memory has a sustain of 1 (immediate), CPU has 5 — a single
        // memory spike escalates while CPU spikes are still gated.
        let t = MetricThresholds(
            cpuWarn: 0.75, cpuCritical: 0.90,
            memWarn: 0.75, memCritical: 0.90,
            diskWarn: 0.85, diskCritical: 0.95,
            cpuSustainSamples: 5,
            memSustainSamples: 1,
            diskSustainSamples: 1
        )
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", thresholds: t)
        vm.ingest(makeSample(cpuPct: 95, memFrac: 0.95, diskFrac: 0.3))
        XCTAssertEqual(vm.tint(for: .cpu), .nominal)
        XCTAssertEqual(vm.tint(for: .mem), .critical)
        XCTAssertEqual(vm.state, .critical)
    }

    func testEffectiveThresholdsInheritsSustainFromGlobal() {
        // Per-node `customThresholds` overrides the warn/critical levels,
        // but sustain always comes from the global. Without this, a node
        // override saved before the sustain UI existed would silently pin
        // sustain=1 and bypass the user's "after N consecutive samples".
        let global = MetricThresholds(
            cpuWarn: 0.75, cpuCritical: 0.90,
            memWarn: 0.75, memCritical: 0.90,
            diskWarn: 0.85, diskCritical: 0.95,
            cpuSustainSamples: 3,
            memSustainSamples: 4,
            diskSustainSamples: 5
        )
        let override = MetricThresholds(
            cpuWarn: 0.50, cpuCritical: 0.80,
            memWarn: 0.50, memCritical: 0.80,
            diskWarn: 0.60, diskCritical: 0.90,
            cpuSustainSamples: 1,
            memSustainSamples: 1,
            diskSustainSamples: 1
        )
        let eff = MetricThresholds.effective(global: global, override: override)
        XCTAssertEqual(eff.cpuWarn, 0.50)
        XCTAssertEqual(eff.cpuCritical, 0.80)
        XCTAssertEqual(eff.cpuSustainSamples, 3)
        XCTAssertEqual(eff.memSustainSamples, 4)
        XCTAssertEqual(eff.diskSustainSamples, 5)

        // No override → use the global thresholds wholesale.
        let nilOverride = MetricThresholds.effective(global: global, override: nil)
        XCTAssertEqual(nilOverride, global)
    }

    func testMarkOfflineSetsState() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t = Date()
        vm.markOffline(reason: "timeout", at: t)
        if case .offline(let reason) = vm.state {
            XCTAssertEqual(reason, "timeout")
        } else {
            XCTFail("expected offline state")
        }
        XCTAssertEqual(vm.lastSeen, t)
    }
}
