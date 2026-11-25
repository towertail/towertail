import XCTest
@testable import Towertail

@MainActor
final class RulesDiffTests: XCTestCase {
    private var settings: ServerSettings!
    private var dir: URL!

    override func setUp() async throws {
        try await super.setUp()
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-diff-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        settings = ServerSettings(url: dir.appendingPathComponent("settings.json"))
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: dir)
        try await super.tearDown()
    }

    private func cloudHost(_ name: String = "web") -> Node {
        Node(displayName: name, kind: .ssh, sshUser: "u", sshHost: name, cloud: CloudLink(enrolledAt: Date()))
    }

    /// Cloud rules that equal the local rules.
    private func matching(hosts: [Node] = []) -> RemoteRulesView {
        RemoteRulesView(
            org: RemoteOrgRules(thresholds: settings.thresholds, alerts: settings.alertRules,
                                muted: settings.mutedSignals, longProc: settings.longProc),
            hosts: hosts.map { .init(nodeId: $0.id, host: RemoteHostRules(), effective: RemoteEffectiveRules()) }
        )
    }

    func testOrgPairDiff() {
        settings.thresholds.cpuWarn = 0.75
        settings.thresholds.cpuCritical = 0.9
        var cloud = matching()
        cloud.org.thresholds.cpuWarn = 0.8
        cloud.org.thresholds.cpuCritical = 0.9
        let rows = RulesDiff.rows(settings: settings, nodes: [], cloud: cloud)
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].scope, .org)
        XCTAssertEqual(rows[0].rule, "CPU warn / critical")
        XCTAssertEqual(rows[0].cloudNow, "80 / 90")
        XCTAssertEqual(rows[0].fromMac, "75 / 90")
        XCTAssertEqual(rows[0].change, .orgPair(.cpu, ThresholdPair(warn: 0.75, critical: 0.9)))
    }

    func testEqualRulesGiveNoRow() {
        let host = cloudHost()
        XCTAssertTrue(RulesDiff.rows(settings: settings, nodes: [host], cloud: matching(hosts: [host])).isEmpty)
    }

    func testOrgAlertAndMuteAndLongProcRows() {
        var cloud = matching()
        cloud.org.alerts.cpu = AlertRule(sustainSeconds: 0, notify: settings.alertRules.cpu.notify)
        cloud.org.muted.set(.disk, muted: true)
        cloud.org.longProc.cpuPct = 90
        let rows = RulesDiff.rows(settings: settings, nodes: [], cloud: cloud)
        XCTAssertEqual(rows.map(\.rule), ["CPU alert after", "Disk mute", "Long-running rule"])
        XCTAssertEqual(rows[0].cloudNow, "at once")
        XCTAssertEqual(rows[0].fromMac, "5 min")
        XCTAssertEqual(rows[1].fromMac, "not muted")
        XCTAssertEqual(rows[1].change, .orgMute(.disk, false))
    }

    func testHostOverrideAgainstOrg() {
        let host = cloudHost("sling-master")
        var node = host
        node.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        var cloud = matching(hosts: [node])
        cloud.org.thresholds.memWarn = 0.8
        cloud.org.thresholds.memCritical = 0.9
        settings.thresholds.memWarn = 0.8
        settings.thresholds.memCritical = 0.9
        let rows = RulesDiff.rows(settings: settings, nodes: [node], cloud: cloud)
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].scope, .host(node.id, "sling-master"))
        XCTAssertEqual(rows[0].rule, "Memory warn / critical")
        XCTAssertEqual(rows[0].cloudNow, "org (80 / 90)")
        XCTAssertEqual(rows[0].fromMac, "85 / 95")
    }

    func testEqualHostOverrideGivesNoRow() {
        var node = cloudHost()
        node.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        var cloud = matching(hosts: [node])
        cloud.hosts[0].host.thresholds = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        XCTAssertTrue(RulesDiff.rows(settings: settings, nodes: [node], cloud: cloud).isEmpty)
    }

    func testSkipsLocalAndSSHHosts() {
        var ssh = Node(displayName: "db", kind: .ssh, sshUser: "u", sshHost: "db")
        ssh.thresholdOverrides = ThresholdOverrides(cpu: ThresholdPair(warn: 0.5, critical: 0.6))
        var local = Node.localMac()
        local.thresholdOverrides = ThresholdOverrides(cpu: ThresholdPair(warn: 0.5, critical: 0.6))
        var unknown = cloudHost("ghost")
        unknown.thresholdOverrides = ThresholdOverrides(cpu: ThresholdPair(warn: 0.5, critical: 0.6))
        let rows = RulesDiff.rows(settings: settings, nodes: [ssh, local, unknown], cloud: matching())
        XCTAssertTrue(rows.isEmpty, "a host that the cloud does not know has no row")
    }

    func testCustomAlertsSendsOnlyChangedRules() {
        var node = cloudHost()
        var alerts = settings.alertRules
        alerts.cpu = AlertRule(sustainSeconds: 60, notify: alerts.cpu.notify)
        node.customAlerts = alerts
        let rows = RulesDiff.rows(settings: settings, nodes: [node], cloud: matching(hosts: [node]))
        XCTAssertEqual(rows.count, 1, "the memory, disk and health rules equal the global rules")
        XCTAssertEqual(rows[0].change, .hostAlert(node.id, .cpu, AlertRule(sustainSeconds: 60, notify: alerts.cpu.notify)))
        XCTAssertEqual(rows[0].cloudNow, "org (5 min)")
        XCTAssertEqual(rows[0].fromMac, "1 min")
    }

    func testHostMuteLongProcAndIgnoredRows() {
        var node = cloudHost()
        node.mutedSignals = MutedSignals([.cpu])
        node.customLongProc = LongProcRule(enabled: true, cpuPct: 60, sustainSeconds: 600, notify: .all, ignore: [])
        node.ignoredProcs = ["pg_dump"]
        let rows = RulesDiff.rows(settings: settings, nodes: [node], cloud: matching(hosts: [node]))
        XCTAssertEqual(rows.map(\.rule), ["CPU mute", "Long-running rule", "Ignored processes"])
        XCTAssertEqual(rows[1].cloudNow, "org")
        XCTAssertEqual(rows[1].fromMac, "60% · 10 min")
        XCTAssertEqual(rows[2].cloudNow, "—")
        XCTAssertEqual(rows[2].fromMac, "pg_dump")
    }

    func testHealthLimitsRow() {
        settings.thresholds.health.files = ThresholdPair(warn: 0.5, critical: 0.6)
        settings.thresholds.zombiesWarn = 10
        let cloud = matching()
        var other = cloud
        other.org.thresholds = .defaults
        let rows = RulesDiff.rows(settings: settings, nodes: [], cloud: other)
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].rule, "Health limits")
        XCTAssertEqual(rows[0].cloudNow, "none")
        XCTAssertEqual(rows[0].fromMac, "2 limits")
    }

    func testImportBodyKeepsUnpickedCloudValues() {
        var node = cloudHost()
        node.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        node.ignoredProcs = ["pg_dump"]
        settings.thresholds.cpuWarn = 0.75
        settings.thresholds.cpuCritical = 0.9
        var cloud = matching(hosts: [node])
        cloud.org.thresholds.cpuWarn = 0.8
        cloud.org.thresholds.cpuCritical = 0.9
        cloud.hosts[0].host.ignoredProcs = ["old"]
        cloud.hosts[0].host.thresholds = ThresholdOverrides(disk: ThresholdPair(warn: 0.7, critical: 0.8))
        let rows = RulesDiff.rows(settings: settings, nodes: [node], cloud: cloud)
        XCTAssertEqual(rows.count, 3)

        let onlyMem = rows.filter { $0.rule == "Memory warn / critical" }
        let body = RulesDiff.importBody(onlyMem, cloud: cloud, device: "test-mac")
        XCTAssertNil(body.org, "no org row is picked")
        XCTAssertEqual(body.hosts.count, 1)
        let rules = body.hosts[0].rules
        XCTAssertEqual(rules.thresholds?.mem, ThresholdPair(warn: 0.85, critical: 0.95))
        XCTAssertEqual(rules.thresholds?.disk, ThresholdPair(warn: 0.7, critical: 0.8), "the cloud value stays")
        XCTAssertEqual(rules.ignoredProcs, ["old"], "an unpicked row keeps the cloud value")
        XCTAssertEqual(body.client, "mac")
        XCTAssertEqual(body.device, "test-mac")

        let all = RulesDiff.importBody(rows, cloud: cloud, device: "test-mac")
        XCTAssertEqual(all.org?.thresholds.cpuWarn, 0.75)
        XCTAssertEqual(all.hosts[0].rules.ignoredProcs, ["pg_dump"])
        XCTAssertNil(all.org?.origin)
    }
}
