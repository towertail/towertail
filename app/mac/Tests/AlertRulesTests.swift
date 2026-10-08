import XCTest
@testable import Towertail

@MainActor
final class AlertRulesTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func sample(
        at seconds: Int,
        cpu: Double = 10,
        mem: Double = 0.3,
        disk: Double = 0.3,
        swapUsed: Int64 = 0,
        swapTotal: Int64 = 0,
        health: HealthInfo? = nil,
        inodes: Double? = nil
    ) -> Sample {
        let totalMem: Int64 = 16_000_000_000
        let totalDisk: Int64 = 100_000_000_000
        var d = DiskSample(mount: "/", fs: "ext4", used: Int64(Double(totalDisk) * disk), total: totalDisk)
        if let inodes {
            d.inodesUsed = Int64(inodes * 1_000_000)
            d.inodesTotal = 1_000_000
        }
        return Sample(
            v: 1, ts: t0.addingTimeInterval(Double(seconds)),
            host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: cpu, load1: 0, load5: 0, load15: 0, cores: 4),
            mem: MemInfo(used: Int64(Double(totalMem) * mem), total: totalMem),
            swap: MemInfo(used: swapUsed, total: swapTotal),
            disks: [d],
            net: nil,
            health: health,
            errors: []
        )
    }

    private func vm(_ rules: AlertRules = .defaults) -> ServerViewModel {
        ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", alertRules: rules)
    }

    private func rules(cpu: Int = 60, mem: Int = 60, health: Int = 60, tolerance: Double = 0.8) -> AlertRules {
        AlertRules(
            cpu: AlertRule(sustainSeconds: cpu, notify: .all),
            mem: AlertRule(sustainSeconds: mem, notify: .all),
            disk: AlertRule(sustainSeconds: 0, notify: .all),
            health: AlertRule(sustainSeconds: health, notify: .all),
            tolerance: tolerance
        )
    }

    // MARK: sustain window

    func testCardStaysRawWhileAlertIsGated() {
        let v = vm(rules())
        v.ingest(sample(at: 0, cpu: 95))
        XCTAssertEqual(v.state, .critical)
        XCTAssertEqual(v.tint(for: .cpu), .critical)
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
        XCTAssertEqual(v.alertLevel, .nominal)
    }

    func testAlertsOnceWindowIsCovered() {
        let v = vm(rules())
        for s in stride(from: 0, through: 50, by: 10) { v.ingest(sample(at: s, cpu: 95)) }
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
        v.ingest(sample(at: 60, cpu: 95))
        XCTAssertEqual(v.alertTint(for: .cpu), .critical)
        XCTAssertEqual(v.alertLevel, .critical)
    }

    func testToleranceSurvivesOneDip() {
        let v = vm(rules())
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, cpu: s == 30 ? 20 : 95)) }
        XCTAssertEqual(v.alertTint(for: .cpu), .critical)
    }

    func testFullToleranceResetsOnDip() {
        let v = vm(rules(tolerance: 1.0))
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, cpu: s == 30 ? 20 : 95)) }
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
    }

    func testSpikeAfterCalmDoesNotAlert() {
        let v = vm(rules())
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, cpu: 10)) }
        v.ingest(sample(at: 70, cpu: 99))
        v.ingest(sample(at: 80, cpu: 99))
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
    }

    func testCriticalFallsBackToWarnWhenOnlyWarnIsSustained() {
        let v = vm(rules())
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, cpu: s < 30 ? 80 : 95)) }
        XCTAssertEqual(v.alertTint(for: .cpu), .warn)
    }

    func testUnevenPollIsTimeWeighted() {
        // One long critical hold outweighs several short calm samples.
        let v = vm(rules())
        v.ingest(sample(at: 0, cpu: 95))
        v.ingest(sample(at: 55, cpu: 10))
        v.ingest(sample(at: 58, cpu: 10))
        v.ingest(sample(at: 60, cpu: 10))
        XCTAssertEqual(v.alertTint(for: .cpu), .critical)
    }

    func testImmediateRuleUsesLatestSample() {
        let v = vm(rules())
        v.ingest(sample(at: 0, disk: 0.96))
        XCTAssertEqual(v.alertTint(for: .disk), .critical)
        v.ingest(sample(at: 10, disk: 0.5))
        XCTAssertEqual(v.alertTint(for: .disk), .nominal)
    }

    func testOfflineResetsWindow() {
        let v = vm(rules())
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, cpu: 95)) }
        XCTAssertEqual(v.alertLevel, .critical)
        v.markOffline(reason: "timeout", at: t0.addingTimeInterval(65))
        XCTAssertEqual(v.alertLevel, .nominal)
        v.ingest(sample(at: 3600, cpu: 95))
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
    }

    func testRulesArePerMetric() {
        let v = vm(rules(cpu: 300, mem: 0))
        v.ingest(sample(at: 0, cpu: 95, mem: 0.95))
        XCTAssertEqual(v.alertTint(for: .cpu), .nominal)
        XCTAssertEqual(v.alertTint(for: .mem), .critical)
    }

    // MARK: memory pressure

    private func psi(memSome: Double = 0, memFull: Double = 0) -> HealthInfo {
        HealthInfo(procs: 100, psi: PSIInfo(cpuSome: 0, memSome: memSome, memFull: memFull, ioSome: 0, ioFull: 0))
    }

    func testMemoryPressureAlertsAtOnce() {
        let v = vm(rules(mem: 120))
        v.ingest(sample(at: 0, mem: 0.8, health: psi(memFull: 10)))
        XCTAssertEqual(v.memPressured, true)
        XCTAssertEqual(v.alertTint(for: .mem), .critical)
    }

    func testPressureWithoutHighMemoryDoesNotAlertMemory() {
        let v = vm(rules(mem: 120))
        v.ingest(sample(at: 0, mem: 0.3, health: psi(memFull: 10)))
        XCTAssertEqual(v.alertTint(for: .mem), .nominal)
    }

    func testHighMemoryWithoutPressureOnlyWarns() {
        let v = vm(rules(mem: 60))
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, mem: 0.95, health: psi())) }
        XCTAssertEqual(v.memPressured, false)
        XCTAssertEqual(v.alertTint(for: .mem), .warn)
    }

    func testMacPressureLevel() {
        let v = vm(rules(mem: 120))
        v.ingest(sample(at: 0, mem: 0.8, health: HealthInfo(procs: 100, memPressure: 4)))
        XCTAssertEqual(v.alertTint(for: .mem), .critical)
    }

    func testSwapGrowthCountsAsPressure() {
        let v = vm(rules(mem: 120))
        v.ingest(sample(at: 0, mem: 0.8, swapUsed: 0, swapTotal: 8_000_000_000))
        XCTAssertNil(v.memPressured)
        v.ingest(sample(at: 10, mem: 0.8, swapUsed: 200_000_000, swapTotal: 8_000_000_000))
        XCTAssertEqual(v.memPressured, true)
        XCTAssertEqual(v.alertTint(for: .mem), .critical)
        v.ingest(sample(at: 20, mem: 0.8, swapUsed: 200_000_000, swapTotal: 8_000_000_000))
        XCTAssertEqual(v.memPressured, false)
    }

    func testNoPressureSignalUsesSustainOnly() {
        let v = vm(rules(mem: 60))
        for s in stride(from: 0, through: 60, by: 10) { v.ingest(sample(at: s, mem: 0.95)) }
        XCTAssertNil(v.memPressured)
        XCTAssertEqual(v.alertTint(for: .mem), .critical)
    }

    // MARK: health

    func testInodesBypassHealthSustain() {
        let v = vm(rules(health: 300))
        v.ingest(sample(at: 0, inodes: 0.97))
        XCTAssertEqual(v.alertTint(for: .health), .critical)
    }

    func testProcessCountUsesHealthSustain() {
        let v = vm(rules(health: 60))
        v.ingest(sample(at: 0, health: HealthInfo(procs: 25_000)))
        XCTAssertEqual(v.tint(for: .health), .critical)
        XCTAssertEqual(v.alertTint(for: .health), .nominal)
    }

    // MARK: rules

    func testNotifyLevels() {
        XCTAssertFalse(AlertNotify.off.allows(.critical))
        XCTAssertFalse(AlertNotify.critical.allows(.warn))
        XCTAssertTrue(AlertNotify.critical.allows(.critical))
        XCTAssertTrue(AlertNotify.all.allows(.warn))
        XCTAssertFalse(AlertNotify.all.allows(.nominal))
    }

    func testDefaults() {
        let d = AlertRules.defaults
        XCTAssertEqual(d.cpu, AlertRule(sustainSeconds: 300, notify: .critical))
        XCTAssertEqual(d.mem, AlertRule(sustainSeconds: 120, notify: .all))
        XCTAssertEqual(d.disk, AlertRule(sustainSeconds: 0, notify: .all))
        XCTAssertEqual(d.health, AlertRule(sustainSeconds: 300, notify: .all))
        XCTAssertEqual(d.tolerance, 0.8)
    }

    func testPartialRulesDecodeWithDefaults() throws {
        let json = #"{"cpu":{"sustainSeconds":60},"tolerance":2}"#
        let r = try JSONDecoder().decode(AlertRules.self, from: Data(json.utf8))
        XCTAssertEqual(r.cpu, AlertRule(sustainSeconds: 60, notify: .critical))
        XCTAssertEqual(r.mem, AlertRules.defaults.mem)
        XCTAssertEqual(r.tolerance, 1.0)
    }

    func testMigratesLegacySampleCounts() throws {
        let json = """
        {"nodes":[],"cardDensity":"a","notificationsEnabled":true,"notifyWarn":true,
         "notifyCritical":true,"notifyDebounceSeconds":60,"sshPollingIntervalSeconds":15,
         "thresholds":{"cpuWarn":0.75,"cpuCritical":0.9,"memWarn":0.75,"memCritical":0.9,
           "diskWarn":0.85,"diskCritical":0.95,"cpuSustainSamples":4,"memSustainSamples":1,
           "diskSustainSamples":2}}
        """
        let p = try JSONDecoder().decode(PersistedSettings.self, from: Data(json.utf8))
        XCTAssertEqual(p.thresholds.alerts.cpu.sustainSeconds, 60)
        XCTAssertEqual(p.thresholds.alerts.mem.sustainSeconds, 120)
        XCTAssertEqual(p.thresholds.alerts.disk.sustainSeconds, 30)
        XCTAssertNil(p.thresholds.legacySustain)

        let out = String(decoding: try JSONEncoder().encode(p), as: UTF8.self)
        XCTAssertTrue(out.contains("\"alerts\""))
        XCTAssertFalse(out.contains("SustainSamples"))
    }

    func testAlertsRoundTrip() throws {
        var p = PersistedSettings.defaults
        p.thresholds.alerts.cpu = AlertRule(sustainSeconds: 900, notify: .off)
        var node = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "db")
        node.customAlerts = AlertRules.defaults
        node.customAlerts?.mem.sustainSeconds = 30
        p.nodes = [node]
        let back = try JSONDecoder().decode(PersistedSettings.self, from: JSONEncoder().encode(p))
        XCTAssertEqual(back.thresholds.alerts.cpu, AlertRule(sustainSeconds: 900, notify: .off))
        XCTAssertEqual(back.nodes.first?.customAlerts?.mem.sustainSeconds, 30)
    }

    func testOldNodeOverrideDoesNotSetAlerts() throws {
        let json = """
        {"id":"\(UUID().uuidString)","displayName":"x","kind":"ssh",
         "customThresholds":{"cpuWarn":0.5,"cpuCritical":0.8,"memWarn":0.5,"memCritical":0.8,
           "diskWarn":0.6,"diskCritical":0.9,"cpuSustainSamples":1}}
        """
        let node = try JSONDecoder().decode(Node.self, from: Data(json.utf8))
        XCTAssertEqual(node.customThresholds?.cpuWarn, 0.5)
        XCTAssertNil(node.customAlerts)
    }
}
