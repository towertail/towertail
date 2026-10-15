import SQLite3
import XCTest
@testable import Towertail

@MainActor
final class HealthStatusTests: XCTestCase {
    private let defaults = MetricThresholds.defaults

    private func sample(health: HealthInfo?, disks: [DiskSample]? = nil, procs: ProcList? = nil) -> Sample {
        Sample(
            v: 1, ts: Date(),
            host: HostInfo(name: "h", os: "linux", arch: "amd64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 10, load1: 0, load5: 0, load15: 0, cores: 4),
            mem: MemInfo(used: 1, total: 10),
            swap: MemInfo(used: 0, total: 0),
            disks: disks,
            procs: procs,
            health: health,
            errors: []
        )
    }

    // MARK: decoding

    func testDecodesHealthSkippedAndInodes() throws {
        let json = """
        {
          "v": 1, "ts": "2026-09-29T10:00:00.000Z",
          "host": { "name": "top", "os": "linux", "arch": "amd64", "kernel": "7.0", "uptime_s": 1, "sampler": "x" },
          "cpu": { "pct": 1, "load_1": 0, "load_5": 0, "load_15": 0, "cores": 8 },
          "mem": { "used": 1, "total": 2 }, "swap": { "used": 0, "total": 0 },
          "disks": [ { "mount": "/", "fs": "ext4", "used": 1, "total": 2, "inodes_used": 90, "inodes_total": 100 } ],
          "procs": { "root": false, "top_n": 20, "total": 34983, "visible": 0, "skipped": true, "items": [] },
          "health": {
            "procs": 34983, "zombies": 33817,
            "zombie_parents": [ { "pid": 2661215, "name": "firebolt", "count": 33817 } ],
            "pids_used": 35500, "pids_max": 232541,
            "files_used": 8928, "files_max": 9223372036854775807,
            "psi": { "cpu_some": 0.01, "mem_some": 0, "mem_full": 0, "io_some": 72.7, "io_full": 66.5 }
          },
          "errors": []
        }
        """.data(using: .utf8)!
        let s = try SampleCodec.decoder().decode(Sample.self, from: json)
        XCTAssertEqual(s.procs?.skipped, true)
        XCTAssertEqual(s.disks?.first?.inodesUsed, 90)
        XCTAssertEqual(s.health?.procs, 34983)
        XCTAssertEqual(s.health?.zombieParents?.first?.name, "firebolt")
        XCTAssertEqual(s.health?.filesMax, Int64.max)
        XCTAssertEqual(s.health?.psi?.ioFull ?? 0, 66.5, accuracy: 0.001)
        XCTAssertNil(s.health?.memPressure)
    }

    func testOldSampleWithoutHealthDecodes() throws {
        let json = """
        {
          "v": 1, "ts": "2026-09-29T10:00:00.000Z",
          "host": { "name": "h", "os": "linux", "arch": "amd64", "kernel": "7.0", "uptime_s": 1, "sampler": "x" },
          "cpu": { "pct": 1, "load_1": 0, "load_5": 0, "load_15": 0, "cores": 8 },
          "mem": { "used": 1, "total": 2 }, "swap": { "used": 0, "total": 0 },
          "disks": [ { "mount": "/", "fs": "ext4", "used": 1, "total": 2 } ],
          "procs": { "root": false, "top_n": 20, "total": 3, "visible": 3, "items": [] },
          "errors": []
        }
        """.data(using: .utf8)!
        let s = try SampleCodec.decoder().decode(Sample.self, from: json)
        XCTAssertNil(s.health)
        XCTAssertNil(s.procs?.skipped)
        XCTAssertNil(s.disks?.first?.inodesTotal)
    }

    // MARK: rules

    func testQuietHostIsNominal() {
        let h = HealthStatus(info: HealthInfo(procs: 300, zombies: 0), disks: nil, thresholds: defaults)
        XCTAssertEqual(h.tint, .nominal)
        XCTAssertTrue(h.reasons.isEmpty)
        XCTAssertNil(h.summary)
    }

    func testProcessAndZombieLevels() {
        let warn = HealthStatus(info: HealthInfo(procs: 5000, zombies: 200), disks: nil, thresholds: defaults)
        XCTAssertEqual(warn.tint(of: .procs), .warn)
        XCTAssertEqual(warn.tint(of: .zombies), .warn)

        let crit = HealthStatus(
            info: HealthInfo(procs: 34983, zombies: 33817,
                             zombieParents: [ZombieParent(pid: 1, name: "firebolt", count: 33817)]),
            disks: nil, thresholds: defaults)
        XCTAssertEqual(crit.tint, .critical)
        XCTAssertTrue(crit.reasons.contains { $0.signal == .zombies && $0.text.hasSuffix("zombies (firebolt)") })
    }

    func testFixedLimitRules() {
        let info = HealthInfo(
            procs: 10,
            pidsUsed: 95, pidsMax: 100,
            filesUsed: 81, filesMax: 100,
            psi: PSIInfo(cpuSome: 99, memSome: 12, memFull: 1, ioSome: 70, ioFull: 25)
        )
        let disks = [DiskSample(mount: "/data", fs: "ext4", used: 1, total: 2, inodesUsed: 96, inodesTotal: 100)]
        let h = HealthStatus(info: info, disks: disks, thresholds: defaults)
        XCTAssertEqual(h.tint(of: .pids), .critical)
        XCTAssertEqual(h.tint(of: .files), .warn)
        XCTAssertEqual(h.tint(of: .memPressure), .warn)
        XCTAssertEqual(h.tint(of: .ioPressure), .warn)
        XCTAssertEqual(h.tint(of: .inodes), .critical)
        XCTAssertTrue(h.reasons.contains { $0.text == "Inodes 96% on /data" })
        // CPU pressure never sets a tint on its own.
        XCTAssertFalse(h.reasons.contains { $0.text.contains("CPU") })
    }

    func testMemFullIsCriticalAndMacPressureLevels() {
        let linux = HealthStatus(
            info: HealthInfo(procs: 1, psi: PSIInfo(cpuSome: 0, memSome: 50, memFull: 25, ioSome: 0, ioFull: 0)),
            disks: nil, thresholds: defaults)
        XCTAssertEqual(linux.tint(of: .memPressure), .critical)
        XCTAssertEqual(linux.reasons.filter { $0.signal == .memPressure }.count, 1)

        let macWarn = HealthStatus(info: HealthInfo(procs: 1, memPressure: 2), disks: nil, thresholds: defaults)
        XCTAssertEqual(macWarn.tint, .warn)
        let macCrit = HealthStatus(info: HealthInfo(procs: 1, memPressure: 4), disks: nil, thresholds: defaults)
        XCTAssertEqual(macCrit.tint, .critical)
        let macOK = HealthStatus(info: HealthInfo(procs: 1, memPressure: 1), disks: nil, thresholds: defaults)
        XCTAssertEqual(macOK.tint, .nominal)
    }

    func testCriticalReasonsComeFirstAndSummary() {
        let info = HealthInfo(procs: 6000, pidsUsed: 95, pidsMax: 100)
        let h = HealthStatus(info: info, disks: nil, thresholds: defaults)
        XCTAssertEqual(h.reasons.map(\.tint), [.critical, .warn])
        XCTAssertEqual(h.summary, "PIDs 95% of limit +1 more")
    }

    func testCustomProcessThresholds() {
        var t = defaults
        t.procsWarn = 100
        t.procsCritical = 200
        let h = HealthStatus(info: HealthInfo(procs: 150), disks: nil, thresholds: t)
        XCTAssertEqual(h.tint(of: .procs), .warn)
    }

    // MARK: view model

    func testHealthDrivesHostStateAndSkippedTable() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.ingest(sample(health: HealthInfo(procs: 300, zombies: 0)))
        XCTAssertEqual(vm.state, .online)
        XCTAssertEqual(vm.procCount.latest?.v, 300)

        let skipped = ProcList(root: false, topN: 20, total: 34983, visible: 0, skipped: true, items: [])
        vm.ingest(sample(health: HealthInfo(procs: 34983, zombies: 33817), procs: skipped))
        XCTAssertEqual(vm.state, .critical)
        XCTAssertEqual(vm.tint(for: .health), .critical)
        XCTAssertEqual(vm.procsSkippedTotal, 34983)
        XCTAssertNil(vm.procs.latest, "skipped samples must not add an empty snapshot")
    }

    func testProcCountFallsBackToProcsTotal() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let procs = ProcList(root: false, topN: 20, total: 42, visible: 42, items: [])
        let point = vm.ingest(sample(health: nil, procs: procs))
        XCTAssertEqual(point.procs, 42)
        XCTAssertEqual(vm.health.tint, .nominal)
    }

    // MARK: thresholds

    func testThresholdsWithoutHealthKeysLoadDefaults() throws {
        let json = """
        { "cpuWarn": 0.7, "cpuCritical": 0.9, "memWarn": 0.7, "memCritical": 0.9, "diskWarn": 0.8, "diskCritical": 0.9 }
        """.data(using: .utf8)!
        let m = try JSONDecoder().decode(MetricThresholds.self, from: json)
        XCTAssertEqual(m.procsWarn, 5000)
        XCTAssertEqual(m.procsCritical, 20000)
        XCTAssertEqual(m.zombiesWarn, 200)
        XCTAssertEqual(m.zombiesCritical, 2000)
        let p = try JSONDecoder().decode(PersistedThresholds.self, from: json)
        XCTAssertEqual(MetricThresholds(p), m)
    }

    func testThresholdsClampWarnBelowCritical() {
        let m = MetricThresholds(
            cpuWarn: 0.7, cpuCritical: 0.9, memWarn: 0.7, memCritical: 0.9, diskWarn: 0.8, diskCritical: 0.9,
            procsWarn: 0, procsCritical: -5, zombiesWarn: 500, zombiesCritical: 100)
        XCTAssertEqual(m.procsWarn, 1)
        XCTAssertEqual(m.procsCritical, 1)
        XCTAssertEqual(m.zombiesCritical, 500)
    }

    func testEffectiveAppliesOnlyOverriddenMetrics() {
        var global = defaults
        global.procsWarn = 9000
        global.procsCritical = 30000
        global.health.pids = ThresholdPair(warn: 0.5, critical: 0.6)
        let override = ThresholdOverrides(disk: ThresholdPair(warn: 0.9, critical: 0.98),
                                          zombies: ThresholdPair(warn: 10, critical: 20))
        let eff = MetricThresholds.effective(global: global, override: override)
        XCTAssertEqual(eff.cpuWarn, global.cpuWarn)
        XCTAssertEqual(eff.diskWarn, 0.9)
        XCTAssertEqual(eff.diskCritical, 0.98)
        XCTAssertEqual(eff.procsWarn, 9000)
        XCTAssertEqual(eff.zombiesWarn, 10)
        XCTAssertEqual(eff.zombiesCritical, 20)
        XCTAssertEqual(eff.health.pids, global.health.pids)
    }

    func testOverridesApplyToHealthLimits() throws {
        let override = ThresholdOverrides(inodes: ThresholdPair(warn: 0.5, critical: 0.6),
                                          memPressure: ThresholdPair(warn: 30, critical: 50))
        let eff = MetricThresholds.effective(global: defaults, override: override)
        XCTAssertEqual(eff.health.inodes, ThresholdPair(warn: 0.5, critical: 0.6))
        XCTAssertEqual(eff.health.memPressure, ThresholdPair(warn: 30, critical: 50))
        XCTAssertEqual(eff.health.pids, HealthLimits.defaults.pids)

        let data = try JSONEncoder().encode(override)
        XCTAssertEqual(try JSONDecoder().decode(ThresholdOverrides.self, from: data), override)
        XCTAssertEqual(override.overridden, [.inodes, .memPressure])
    }

    func testLegacyCustomThresholdsMigrateToOverrides() throws {
        let json = """
        { "id": "6F9619FF-8B86-D011-B42D-00C04FC964FF", "displayName": "db", "kind": "ssh",
          "customThresholds": { "cpuWarn": 0.5, "cpuCritical": 0.6, "memWarn": 0.5, "memCritical": 0.6,
                                "diskWarn": 0.5, "diskCritical": 0.6, "procsWarn": 1 } }
        """.data(using: .utf8)!
        let node = try JSONDecoder().decode(Node.self, from: json)
        XCTAssertEqual(node.thresholdOverrides?.overridden, [.cpu, .mem, .disk])
        XCTAssertEqual(node.thresholdOverrides?.cpu, ThresholdPair(warn: 0.5, critical: 0.6))

        let again = try JSONDecoder().decode(Node.self, from: JSONEncoder().encode(node))
        XCTAssertEqual(again.thresholdOverrides, node.thresholdOverrides)
    }

    func testEmptyOverridesStoreAsNil() {
        let node = Node(displayName: "a", kind: .local, thresholdOverrides: ThresholdOverrides())
        XCTAssertNil(node.thresholdOverrides)
    }

    func testHealthLimitsDriveReasons() {
        var t = defaults
        t.health.pids = ThresholdPair(warn: 0.1, critical: 0.2)
        let info = HealthInfo(procs: 10, pidsUsed: 15, pidsMax: 100)
        let s = HealthStatus(info: info, disks: nil, thresholds: t)
        XCTAssertEqual(s.tint(of: .pids), .warn)
    }

    func testThresholdsWithoutHealthLimitsLoadDefaults() throws {
        let json = """
        { "cpuWarn": 0.7, "cpuCritical": 0.9, "memWarn": 0.7, "memCritical": 0.9, "diskWarn": 0.8, "diskCritical": 0.9,
          "health": { "inodes": { "warn": 0.5, "critical": 0.7 } } }
        """.data(using: .utf8)!
        let m = try JSONDecoder().decode(MetricThresholds.self, from: json)
        XCTAssertEqual(m.health.inodes, ThresholdPair(warn: 0.5, critical: 0.7))
        XCTAssertEqual(m.health.pids, HealthLimits.defaults.pids)
    }

    // MARK: history

    func testHistoryRoundTripsProcsAndMigratesOldTable() async throws {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("towertail-health-\(UUID().uuidString).sqlite")
        defer { try? FileManager.default.removeItem(at: url) }

        // A database from before the procs column existed.
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(url.path, &db), SQLITE_OK)
        sqlite3_exec(db, """
            CREATE TABLE samples (node_id TEXT NOT NULL, ts REAL NOT NULL, cpu REAL, mem REAL,
                                  disk REAL, net REAL, rx_mbps REAL, tx_mbps REAL);
            """, nil, nil, nil)
        sqlite3_close(db)

        let store = HistoryStore(url: url)
        let id = UUID()
        let t = Date(timeIntervalSince1970: 1_700_000_000)
        store.append(nodeID: id, point: HistoryPoint(
            t: t, cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil))
        store.append(nodeID: id, point: HistoryPoint(
            t: t.addingTimeInterval(1), cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil, procs: 34983))
        try await Task.sleep(nanoseconds: 200_000_000)

        let rows = store.loadRecent(nodeID: id)
        XCTAssertEqual(rows.count, 2)
        XCTAssertNil(rows.first?.procs)
        XCTAssertEqual(rows.last?.procs, 34983)
    }
}
