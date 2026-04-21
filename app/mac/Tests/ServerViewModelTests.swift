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
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, agent: "x", machineID: nil),
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
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "k", uptimeS: 1, agent: "x", machineID: nil),
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
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, rxBps: 2 * oneMB, txBps: oneMB))
        XCTAssertEqual(vm.netRxMBps, 2.0, accuracy: 0.0001)
        XCTAssertEqual(vm.netTxMBps, 1.0, accuracy: 0.0001)
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
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, agent: "x", machineID: nil),
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
                host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, agent: "x", machineID: nil),
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
