import XCTest
@testable import Towertail

final class LiveWireDecodingTests: XCTestCase {
    private func fixture(_ name: String) throws -> Data {
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        let url = root.appendingPathComponent("server/testdata/fixtures/\(name)")
        guard FileManager.default.fileExists(atPath: url.path) else {
            throw XCTSkip("missing \(url.path)")
        }
        return try Data(contentsOf: url)
    }

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try SampleCodec.decoder().decode(T.self, from: Data(json.utf8))
    }

    func testDecodeSummaryFixture() throws {
        let s = try SampleCodec.decoder().decode(LiveSummary.self, from: fixture("summary_linux.json"))
        XCTAssertEqual(s.cpu, 23.4, accuracy: 0.0001)
        XCTAssertEqual(s.mem, 0.352, accuracy: 0.0001)
        XCTAssertEqual(s.disk, 0.3, accuracy: 0.0001)
        XCTAssertEqual(s.rx, 1_250_000)
        XCTAssertEqual(s.tx, 3_750_000)
        XCTAssertEqual(s.procs, 312)
        XCTAssertEqual(s.host?.sampler, "towertail-sampler/0.4.0")
        XCTAssertEqual(s.host?.memTotal, 33_554_432_000)
        XCTAssertEqual(s.ts, SampleCodec.parseRFC3339UTC("2026-10-17T12:00:00.000Z"))
    }

    func testDecodeSummaryWithoutOptionalKeys() throws {
        let s = try decode(LiveSummary.self, """
        {"ts":"2026-10-17T12:00:00.000Z","cpu":1,"mem":0.5,"disk":0.2,"rx":0,"tx":0,"load":0.1}
        """)
        XCTAssertNil(s.procs)
        XCTAssertNil(s.health)
        XCTAssertNil(s.host)
    }

    func testDecodeDetailProcesses() throws {
        let d = try SampleCodec.decoder().decode(LiveDetail.self, from: fixture("detail_processes_linux.json"))
        XCTAssertFalse(d.procs?.items.isEmpty ?? true)
        XCTAssertNil(d.ports)
        XCTAssertNil(d.disks)
        XCTAssertNil(d.diskIO)
        XCTAssertNil(d.health)
    }

    func testDecodeDetailDiskIOKey() throws {
        let d = try decode(LiveDetail.self, """
        {"ts":"2026-10-17T12:00:00Z","disk_io":{"read_bps":1,"write_bps":2,"read_cum":3,"write_cum":4}}
        """)
        XCTAssertEqual(d.diskIO?.writeBps, 2)
    }

    func testDecodeHealthBriefWithInodes() throws {
        let s = try decode(LiveSummary.self, """
        {"ts":"2026-10-17T12:00:00Z","cpu":1,"mem":0.5,"disk":0.2,"rx":0,"tx":0,"load":0,
         "health":{"procs":300,"zombies":2,"inodes":0.93}}
        """)
        XCTAssertEqual(s.health?.info.procs, 300)
        XCTAssertEqual(s.health?.info.zombies, 2)
        XCTAssertNil(s.health?.info.zombieParents)
        XCTAssertEqual(s.health?.inodes, 0.93)
    }

    private func encode(_ view: LiveView) throws -> [String: Any] {
        let data = try JSONEncoder().encode(view)
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    func testEncodeViewAndEmptyView() throws {
        let a = UUID(), b = UUID()
        let full = try encode(LiveView(hosts: [a, b], detail: ViewDetail(host: a, tab: .processes)))
        XCTAssertEqual(full["type"] as? String, "view")
        XCTAssertEqual(full["hosts"] as? [String], [a.uuidString, b.uuidString])
        XCTAssertEqual(full["ttl"] as? Int, 60)
        let detail = try XCTUnwrap(full["detail"] as? [String: String])
        XCTAssertEqual(detail, ["host": a.uuidString, "tab": "processes"])

        let empty = try encode(.empty)
        XCTAssertEqual(empty["type"] as? String, "view")
        XCTAssertEqual((empty["hosts"] as? [Any])?.count, 0)
        XCTAssertNil(empty["detail"])
    }

    @MainActor
    func testStreamMessageDecodesLeaseFrames() throws {
        let id = UUID()
        let summary = """
        {"type":"summary","nodeId":"\(id.uuidString)","summary":{"ts":"2026-10-17T12:00:00Z","cpu":1,"mem":0.5,"disk":0.2,"rx":0,"tx":0,"load":0}}
        """
        guard case .summary(let sid, _) = StreamMessage.decode(summary) else { return XCTFail("not a summary") }
        XCTAssertEqual(sid, id)
        let detail = """
        {"type":"detail","nodeId":"\(id.uuidString)","tab":"ports","detail":{"ts":"2026-10-17T12:00:00Z"}}
        """
        guard case .detail(_, let tab, _) = StreamMessage.decode(detail) else { return XCTFail("not a detail") }
        XCTAssertEqual(tab, .ports)
        guard case .lease(let notice) = StreamMessage.decode(#"{"type":"lease","lease":{"state":"ended","reason":"limit"}}"#) else {
            return XCTFail("not a lease")
        }
        XCTAssertTrue(notice.isLimit)
        guard case .hello = StreamMessage.decode(#"{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}"#) else {
            return XCTFail("not a hello")
        }
    }

    @MainActor
    func testStreamEventAlertFields() {
        let node = UUID(), alert = UUID()
        let event = """
        {"type":"event","event":{"id":"\(UUID().uuidString)","alertId":"\(alert.uuidString)","ts":"2026-11-25T10:00:00Z","kind":"threshold_crossed","nodeId":"\(node.uuidString)","metric":"cpu","tint":"critical","notify":true,"title":"web: CPU critical","text":"CPU at 97%."}}
        """
        guard case .event(let e) = StreamMessage.decode(event) else { return XCTFail("not an event") }
        XCTAssertEqual(e.alertId, alert)
        XCTAssertEqual(e.notify, true)
        XCTAssertEqual(e.title, "web: CPU critical")
        XCTAssertEqual(e.text, "CPU at 97%.")

        let old = #"{"type":"event","event":{"kind":"sampler_version","nodeId":"\#(node.uuidString)"}}"#
        guard case .event(let o) = StreamMessage.decode(old) else { return XCTFail("not an event") }
        XCTAssertNil(o.alertId)
        XCTAssertNil(o.notify)

        let updated = """
        {"type":"alert_updated","nodeId":"\(node.uuidString)","alert":{"id":"\(alert.uuidString)","node_id":"\(node.uuidString)","kind":"threshold_crossed","metric":"cpu","tint":"critical","ts":"2026-11-25T10:00:00.25Z","value":0.97,"acked_by":"\(UUID().uuidString)","acked_at":"2026-11-25T10:01:00.5Z"}}
        """
        guard case .alertUpdated(let a) = StreamMessage.decode(updated) else { return XCTFail("not an alert_updated") }
        XCTAssertEqual(a.id, alert)
        XCTAssertEqual(a.nodeId, node)
        XCTAssertTrue(a.isSilenced())
    }
}
