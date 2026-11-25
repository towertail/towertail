import XCTest
@testable import Towertail

final class RemoteRulesTests: XCTestCase {
    private let hostA = UUID(uuidString: "22222222-2222-2222-2222-222222222222")!
    private let hostB = UUID(uuidString: "33333333-3333-3333-3333-333333333333")!

    private func fixture() throws -> RemoteRulesView {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "rules-view", withExtension: "json"))
        return try SampleCodec.decoder().decode(RemoteRulesView.self, from: Data(contentsOf: url))
    }

    func testDecodesRulesView() throws {
        let v = try fixture()
        XCTAssertEqual(v.hosts.count, 2)
        XCTAssertEqual(v.org.thresholds.cpuWarn, 0.8)
        XCTAssertEqual(v.org.alerts.cpu, AlertRule(sustainSeconds: 300, notify: .critical))
        XCTAssertTrue(v.org.muted.metrics.contains(.disk))
        XCTAssertEqual(v.org.longProc.cpuPct, 50)
        XCTAssertEqual(v.org.origin?.client, "web")

        let a = try XCTUnwrap(v.hosts.first { $0.nodeId == hostA })
        XCTAssertEqual(a.host.thresholds?.mem, ThresholdPair(warn: 0.85, critical: 0.95))
        XCTAssertEqual(a.host.alerts?.cpu, AlertRule(sustainSeconds: 60, notify: .all))
        XCTAssertTrue(a.host.muted.long)
        XCTAssertEqual(a.host.ignoredProcs, ["pg_dump"])
        XCTAssertEqual(a.host.origin?.source, "fritz-mbp")
        XCTAssertNotNil(a.host.origin?.at, "a time with fractional seconds decodes")
        XCTAssertEqual(a.effective.thresholds.memWarn, 0.85)
        XCTAssertEqual(a.effective.alerts.cpu.sustainSeconds, 60)
        XCTAssertTrue(a.effective.muted.long)
        XCTAssertTrue(a.effective.isHostPair(.mem))
        XCTAssertFalse(a.effective.isHostPair(.cpu))
        XCTAssertEqual(a.effective.sources.alerts["cpu"], "host")
        XCTAssertEqual(a.effective.ignore, [], "a null ignore list is empty")

        let b = try XCTUnwrap(v.hosts.first { $0.nodeId == hostB })
        XCTAssertNil(b.host.thresholds)
        XCTAssertEqual(b.effective.ignore, ["backup"])
    }

    func testViewSurvivesCacheRoundTrip() throws {
        let v = try fixture()
        let back = try JSONDecoder().decode(RemoteRulesView.self, from: JSONEncoder().encode(v))
        XCTAssertEqual(back.hosts.map(\.effective), v.hosts.map(\.effective))
        XCTAssertEqual(back.org.thresholds, v.org.thresholds)
        XCTAssertEqual(back.hosts[0].host.muted, v.hosts[0].host.muted)
    }

    func testOriginIgnoresBadTime() throws {
        let o = try JSONDecoder().decode(RemoteRulesOrigin.self, from: Data(#"{"client":"web","at":"nope"}"#.utf8))
        XCTAssertEqual(o.client, "web")
        XCTAssertNil(o.at)
    }

    func testOriginEncodesClientAndDevice() throws {
        let o = RemoteRulesOrigin(client: "mac", device: "m1", userId: UUID(), at: Date())
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(o)) as? [String: Any])
        XCTAssertEqual(Set(json.keys), ["client", "device"])
    }

    func testHostRulesBodyAlwaysSendsMuted() throws {
        let data = try JSONEncoder().encode(RemoteHostRules())
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(json["muted"] as? [String], [])
        XCTAssertNil(json["thresholds"])
        XCTAssertNil(json["alerts"])
        XCTAssertNil(json["ignoredProcs"])
    }

    func testEmptyOverridesNormalizeToNil() {
        let r = RemoteHostRules(thresholds: ThresholdOverrides(), alerts: RemoteAlertOverrides())
        XCTAssertNil(r.thresholds)
        XCTAssertNil(r.alerts)
    }

    func testMutedSignalsLongRoundTrip() throws {
        let m = MutedSignals([.cpu], long: true)
        XCTAssertFalse(m.isEmpty)
        XCTAssertTrue(MutedSignals(long: false).isEmpty)
        let back = try JSONDecoder().decode(MutedSignals.self, from: JSONEncoder().encode(m))
        XCTAssertEqual(back, m)
        XCTAssertTrue(back.long)
        let names = try JSONDecoder().decode([String].self, from: JSONEncoder().encode(m))
        XCTAssertEqual(names, ["cpu", "long"])
    }

    func testAlertOverridesApplied() {
        let o = RemoteAlertOverrides(cpu: AlertRule(sustainSeconds: 10, notify: .off), tolerance: 0.5)
        let r = o.applied(to: .defaults)
        XCTAssertEqual(r.cpu, AlertRule(sustainSeconds: 10, notify: .off))
        XCTAssertEqual(r.mem, AlertRules.defaults.mem)
        XCTAssertEqual(r.tolerance, 0.5)
    }

    func testAlertOverridesDiffSkipsEqualRules() {
        var host = AlertRules.defaults
        host.cpu = AlertRule(sustainSeconds: 45, notify: .critical)
        let d = RemoteAlertOverrides.diff(host, from: .defaults)
        XCTAssertEqual(d.cpu, host.cpu)
        XCTAssertNil(d.mem)
        XCTAssertNil(d.disk)
        XCTAssertNil(d.health)
        XCTAssertNil(d.tolerance)
        XCTAssertTrue(RemoteAlertOverrides.diff(.defaults, from: .defaults).isEmpty)
    }
}
