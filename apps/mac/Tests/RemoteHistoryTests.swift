import XCTest
@testable import Towertail

@MainActor
final class RemoteHistoryTests: XCTestCase {
    private let node = "11111111-1111-1111-1111-111111111111"

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try SampleCodec.decoder().decode(T.self, from: Data(json.utf8))
    }

    private func response(_ series: String) throws -> RemoteHistoryResponse {
        try decode(RemoteHistoryResponse.self, """
        {"node_id":"\(node)","series":{\(series)},"from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"raw"}
        """)
    }

    func testHistoryPointsJoinSeriesByTimestamp() throws {
        let r = try response("""
        "cpu":[{"ts":"2026-04-20T12:00:00Z","value":50}],
        "net_rx":[{"ts":"2026-04-20T12:00:00Z","value":1048576}],
        "procs":[{"ts":"2026-04-20T12:00:00Z","value":321}]
        """)
        let points = r.historyPoints()
        XCTAssertEqual(points.count, 1)
        XCTAssertEqual(points[0].cpu, 0.5)
        XCTAssertEqual(points[0].rxMBps, 1.0)
        XCTAssertNil(points[0].txMBps)
        XCTAssertEqual(points[0].net ?? 0, 0.01, accuracy: 1e-9)
        XCTAssertEqual(points[0].procs, 321)
    }

    func testHistoryPointsMissingMetricIsNil() throws {
        let r = try response(#""cpu":[{"ts":"2026-04-20T12:00:00Z","value":10},{"ts":"2026-04-20T12:00:10Z","value":20}]"#)
        let points = r.historyPoints()
        XCTAssertEqual(points.map(\.cpu), [0.1, 0.2])
        XCTAssertNil(points[0].procs)
        XCTAssertNil(points[0].mem)
        XCTAssertNil(points[0].net)
    }

    func testDiskCapacityRowsFromMounts() throws {
        let d = try decode(RemoteDiskHistory.self, """
        {"mounts":[{"mount":"/","fs":"ext4","points":[{"ts":"2026-04-20T12:00:00Z","used":50,"total":100}]}]}
        """)
        let rows = d.capacityRows()
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].mount, "/")
        XCTAssertEqual(rows[0].frac, 0.5)
    }

    func testDiskIORowsAreTotalsInMBps() throws {
        let r = try response("""
        "disk_read":[{"ts":"2026-04-20T12:00:00Z","value":2097152}],
        "disk_write":[{"ts":"2026-04-20T12:00:00Z","value":1048576}]
        """)
        let rows = r.diskIORows()
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].device, "")
        XCTAssertEqual(rows[0].readMBps, 2.0)
        XCTAssertEqual(rows[0].writeMBps, 1.0)
    }

    func testServerViewModelMergeKeepsNewerLivePoints() throws {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "linux/arm64")
        let live = Date(timeIntervalSince1970: 1_000)
        vm.cpu.append(MetricPoint(t: live, v: 0.9))
        let old = (0..<3).map { i in
            HistoryPoint(t: live.addingTimeInterval(Double(i - 5) * 10), cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil)
        }
        vm.merge(history: old)
        XCTAssertEqual(vm.cpu.count, 4)
        XCTAssertEqual(vm.cpu.latest?.v, 0.9)
        XCTAssertTrue(vm.mem.isEmpty)
    }
}
