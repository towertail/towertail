import XCTest
@testable import Towertail

@MainActor
final class ServerViewModelTests: XCTestCase {
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

    // MARK: - Stale data

    private func connectedVM() -> (ServerViewModel, Date) {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", kind: .ssh)
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, t: t0), now: t0)
        return (vm, t0)
    }

    func testCheckStaleMarksOfflineAfterLimit() {
        let (vm, t0) = connectedVM()
        vm.checkStale(now: t0.addingTimeInterval(30), limit: 30)
        XCTAssertEqual(vm.state, .online)
        XCTAssertNil(vm.noDataSince)

        vm.checkStale(now: t0.addingTimeInterval(31), limit: 30)
        XCTAssertEqual(vm.state, .offline(reason: "no data for 31s"))
        XCTAssertEqual(vm.noDataSince, t0.addingTimeInterval(31))

        vm.checkStale(now: t0.addingTimeInterval(68 * 3600), limit: 30)
        XCTAssertEqual(vm.state, .offline(reason: "no data for 68h"))
        XCTAssertEqual(vm.noDataSince, t0.addingTimeInterval(31))
    }

    func testNewSampleClearsNoData() {
        let (vm, t0) = connectedVM()
        vm.checkStale(now: t0.addingTimeInterval(60), limit: 30)
        XCTAssertNotNil(vm.noDataSince)
        let t1 = t0.addingTimeInterval(61)
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, t: t1), now: t1)
        XCTAssertNil(vm.noDataSince)
        XCTAssertEqual(vm.state, .online)
    }

    func testStaleKeepsLastError() {
        let (vm, t0) = connectedVM()
        vm.markOffline(reason: "timeout", at: t0.addingTimeInterval(20))
        vm.checkStale(now: t0.addingTimeInterval(40), limit: 30)
        XCTAssertEqual(vm.state, .offline(reason: "no data for 40s (timeout)"))
        vm.markOffline(reason: "timeout", at: t0.addingTimeInterval(45))
        XCTAssertEqual(vm.state, .offline(reason: "no data for 40s (timeout)"))
    }

    func testSuspendedAndNeverConnectedHostsAreSkipped() {
        let fresh = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", kind: .ssh)
        fresh.checkStale(now: Date().addingTimeInterval(3600), limit: 30)
        XCTAssertEqual(fresh.state, .unknown)

        let (vm, t0) = connectedVM()
        vm.markSuspended(reason: "asleep")
        vm.checkStale(now: t0.addingTimeInterval(3600), limit: 30)
        XCTAssertEqual(vm.state, .suspended(reason: "asleep"))
        XCTAssertNil(vm.noDataSince)
    }

    func testNoFalseStaleAfterWake() {
        let (vm, t0) = connectedVM()
        vm.markSuspended(reason: "asleep")
        let wake = t0.addingTimeInterval(8 * 3600)
        vm.clearSuspended(now: wake)
        vm.checkStale(now: wake.addingTimeInterval(10), limit: 30)
        XCTAssertNil(vm.noDataSince)
        vm.checkStale(now: wake.addingTimeInterval(31), limit: 30)
        XCTAssertNotNil(vm.noDataSince)
    }

    func testOldSampleTimestampDoesNotCauseStale() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", kind: .ssh)
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let old = now.addingTimeInterval(-3 * 86_400)
        vm.ingest(makeSample(cpuPct: 10, memFrac: 0.1, diskFrac: 0.1, t: old), now: now)
        vm.checkStale(now: now.addingTimeInterval(10), limit: 30)
        XCTAssertNil(vm.noDataSince)
        XCTAssertEqual(vm.state, .online)
    }

    // MARK: long-running processes

    private func procSample(at t: Date, cpu: Double) -> Sample {
        Sample(
            v: 1, ts: t,
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "k", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 10, load1: 0, load5: 0, load15: 0, cores: 4),
            mem: MemInfo(used: 1, total: 10),
            swap: MemInfo(used: 0, total: 0),
            procs: ProcList(root: false, topN: 20, total: 1, visible: 1,
                            items: [ProcSample(pid: 9, name: "yes", cpuPct: cpu, rss: 1, startTS: Date(timeIntervalSince1970: 1))]),
            health: HealthInfo(procs: 100),
            errors: []
        )
    }

    func testLongProcMakesWarnWithoutHealthAlert() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let node = Node(displayName: "h", kind: .ssh)
        var rule = LongProcRule.defaults
        rule.sustainSeconds = 120
        vm.applyLongProc(node: node, global: rule)
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        for s in stride(from: 0, through: 120, by: 10) {
            vm.ingest(procSample(at: t0.addingTimeInterval(Double(s)), cpu: 100), now: t0.addingTimeInterval(Double(s)))
        }
        XCTAssertEqual(vm.longProcs.map(\.name), ["yes"])
        XCTAssertEqual(vm.state, .warn)
        XCTAssertEqual(vm.health.summary, "yes 100% for 2m")
        XCTAssertEqual(vm.alertTint(for: .health), .nominal)
        XCTAssertEqual(vm.alertLevel, .warn)

        var hostIgnore = node
        hostIgnore.ignoredProcs = ["YES"]
        vm.applyLongProc(node: hostIgnore, global: rule)
        XCTAssertTrue(vm.longProcs.isEmpty)
        XCTAssertEqual(vm.alertLevel, .nominal)
    }

    func testGlobalIgnoreAppliesUnderCustomRule() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        var node = Node(displayName: "h", kind: .ssh)
        node.customLongProc = LongProcRule(enabled: true, cpuPct: 20, sustainSeconds: 60, notify: .all, ignore: [])
        var global = LongProcRule.defaults
        global.ignore = ["Yes"]
        vm.applyLongProc(node: node, global: global)
        XCTAssertEqual(vm.longProcRule.cpuPct, 20)
        XCTAssertEqual(vm.ignoredProcs, ["yes"])
    }

    func testNotifyOffKeepsCardWarnButNotIcon() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        var rule = LongProcRule.defaults
        rule.sustainSeconds = 60
        rule.notify = .off
        vm.applyLongProc(node: Node(displayName: "h", kind: .ssh), global: rule)
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        for s in stride(from: 0, through: 60, by: 10) {
            vm.ingest(procSample(at: t0.addingTimeInterval(Double(s)), cpu: 100), now: t0.addingTimeInterval(Double(s)))
        }
        XCTAssertEqual(vm.state, .warn)
        XCTAssertEqual(vm.alertLevel, .nominal)
    }

    // MARK: - Lease frames

    private func summary(at t: Date = Date(), cpu: Double = 12.5, host: String? = nil, health: String? = nil) throws -> LiveSummary {
        var extra = ""
        if let host { extra += ",\"host\":\(host)" }
        if let health { extra += ",\"health\":\(health)" }
        let json = """
        {"ts":"\(SampleCodec.formatRFC3339UTC(t))","cpu":\(cpu),"mem":0.5,"disk":0.25,"rx":1048576,"tx":2097152,"load":0.3,"procs":120\(extra)}
        """
        return try SampleCodec.decoder().decode(LiveSummary.self, from: Data(json.utf8))
    }

    private func detail(_ body: String) throws -> LiveDetail {
        try SampleCodec.decoder().decode(LiveDetail.self, from: Data("{\"ts\":\"2026-10-17T12:00:00Z\",\(body)}".utf8))
    }

    func testIngestSummaryAppendsSeries() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t = Date(timeIntervalSince1970: 1_800_000_000)
        vm.ingest(summary: try summary(at: t))
        XCTAssertEqual(vm.cpu.count, 1)
        XCTAssertEqual(vm.mem.count, 1)
        XCTAssertEqual(vm.disk.count, 1)
        XCTAssertEqual(vm.net.count, 1)
        XCTAssertEqual(vm.procCount.latest?.v, 120)
        XCTAssertEqual(vm.cpu.latest?.v ?? 0, 0.125, accuracy: 0.0001)
        XCTAssertEqual(vm.netRxMBps, 1.0, accuracy: 0.0001)
        XCTAssertEqual(vm.netTxMBps, 2.0, accuracy: 0.0001)
        XCTAssertEqual(vm.lastSeen, t)
        XCTAssertTrue(vm.everConnected)
        XCTAssertEqual(vm.state, .online)
    }

    func testIngestSummaryAppliesFactsOnlyWhenPresent() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let host = #"{"os":"linux","arch":"amd64","cores":8,"mem_total":1,"uptime_s":1,"sampler":"0.4.0+abc"}"#
        vm.ingest(summary: try summary(host: host))
        XCTAssertEqual(vm.osArch, "Linux · amd64")
        XCTAssertEqual(vm.samplerVersion, "0.4.0+abc")
        vm.ingest(summary: try summary())
        XCTAssertEqual(vm.osArch, "Linux · amd64")
        XCTAssertEqual(vm.samplerVersion, "0.4.0+abc")
    }

    func testSummaryHealthKeepsLastValueAndInodes() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.ingest(summary: try summary(health: #"{"procs":120,"inodes":0.97}"#))
        XCTAssertEqual(vm.health.tint(of: .inodes), .critical)
        XCTAssertEqual(vm.health.reasons.first(where: { $0.signal == .inodes })?.text, "Inodes 97%")
        vm.ingest(summary: try summary())
        XCTAssertEqual(vm.health.tint(of: .inodes), .critical)
    }

    func testApplyDetailProcessesFillsProcSeries() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let d = try detail(#""procs":{"root":true,"top_n":1,"total":1,"visible":1,"items":[{"pid":1,"name":"init","cpu_pct":1.5,"rss":1024}]}"#)
        vm.apply(detail: d, tab: .processes)
        XCTAssertTrue(vm.procsAvailable)
        XCTAssertTrue(vm.procsRoot)
        XCTAssertEqual(vm.procs.count, 1)
        XCTAssertEqual(vm.procs.latest?.items.first?.name, "init")
    }

    func testApplyDetailPortsAndDisks() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let ports = try detail(#""ports":{"root":false,"collected_ts":"2026-10-17T12:00:00Z","max_conn":10,"truncated":false,"total":0,"items":[]}"#)
        vm.apply(detail: ports, tab: .ports)
        XCTAssertTrue(vm.portsAvailable)
        let disks = try detail(#""disks":[{"mount":"/","fs":"ext4","used":50,"total":100}],"disk_io":{"read_bps":5,"write_bps":6,"read_cum":0,"write_cum":0}"#)
        vm.apply(detail: disks, tab: .disks)
        XCTAssertFalse(vm.disksPerMount.mounts.isEmpty)
        XCTAssertEqual(vm.latestDisks.count, 1)
    }

    func testApplyDetailHealthKeepsZombieParents() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let d = try detail(#""health":{"procs":10,"zombies":500,"zombie_parents":[{"pid":7,"name":"bad","count":500}]}"#)
        vm.apply(detail: d, tab: .health)
        XCTAssertEqual(vm.health.info?.zombieParents?.first?.name, "bad")
        XCTAssertTrue(vm.health.reasons.contains { $0.signal == .zombies })
    }

    func testCloudSourceSkipsStaleCheck() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let t = Date()
        vm.ingest(summary: try summary(at: t), now: t)
        vm.liveSource = .cloud
        vm.checkStale(now: t.addingTimeInterval(600), limit: 30)
        XCTAssertFalse(vm.state.isOffline)
        vm.liveSource = .poll
        vm.checkStale(now: t.addingTimeInterval(600), limit: 30)
        XCTAssertTrue(vm.state.isOffline)
    }

    func testMarkReachableLeavesOffline() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.ingest(summary: try summary())
        vm.markOffline(reason: "down", at: Date())
        XCTAssertTrue(vm.state.isOffline)
        vm.markReachable(at: Date())
        XCTAssertFalse(vm.state.isOffline)
        XCTAssertEqual(vm.state, .online)
    }
}
