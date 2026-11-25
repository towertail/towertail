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

    // MARK: - Process snapshot, offenders, process detail

    func testDecodeRawSnapshotHasPid() throws {
        let s = try decode(RemoteProcessSnapshot.self, """
        {"node_id":"\(node)","ts":"2026-04-20T12:00:10Z","tier":"raw","bucket_s":0,"by":"cpu","total":42,
         "prev":"2026-04-20T12:00:00Z","next":"2026-04-20T12:00:20Z",
         "processes":[{"key":"0a.Z28","pid":7,"ppid":1,"user":"root","name":"go","cmd":"go test","cpu_pct":12.5,"rss":2048,"threads":4,"start_ts":"2026-04-20T11:00:00Z"}]}
        """)
        XCTAssertEqual(s.tier, "raw")
        XCTAssertEqual(s.total, 42)
        XCTAssertNotNil(s.prev)
        XCTAssertEqual(s.processes[0].pid, 7)
        XCTAssertEqual(s.processes[0].key, "0a.Z28")
        XCTAssertNil(s.processes[0].cpuPctMax)
        XCTAssertEqual(s.processes[0].id, "7#0a.Z28")
    }

    func testDecodeMinuteSnapshotHasNoPid() throws {
        let s = try decode(RemoteProcessSnapshot.self, """
        {"node_id":"\(node)","ts":"2026-04-20T12:00:00Z","tier":"1m","bucket_s":60,"by":"mem",
         "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","cpu_pct":10,"cpu_pct_max":88,"rss":2048}]}
        """)
        XCTAssertEqual(s.bucketS, 60)
        XCTAssertNil(s.total)
        XCTAssertNil(s.processes[0].pid)
        XCTAssertEqual(s.processes[0].cpuPctMax, 88)
        XCTAssertEqual(s.processes[0].id, "0a.Z28")
    }

    func testDecodeOffendersWithSeries() throws {
        let r = try decode(RemoteOffenders.self, """
        {"node_id":"\(node)","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",
         "series_from":"2026-04-20T11:00:00Z","series_step_s":60,
         "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","repo":"towertail","cpu_pct_avg":12.5,"cpu_pct_max":90,"rss_max":2048,
          "series":{"cpu":[1,2,3],"rss":[10,20,30],"io":[0,5,0]}},
          {"key":"0b.bm9kZQ","name":"node","cpu_pct_avg":1,"cpu_pct_max":2,"rss_max":1}]}
        """)
        XCTAssertEqual(r.seriesStepS, 60)
        XCTAssertEqual(r.processes[0].series?.cpu, [1, 2, 3])
        XCTAssertEqual(r.processes[0].series?.io, [0, 5, 0])
        XCTAssertEqual(r.processes[0].repo, "towertail")
        XCTAssertNil(r.processes[1].series)
        XCTAssertNotEqual(r.processes[0].id, r.processes[1].id)
    }

    func testDecodeProcessDetail() throws {
        let d = try decode(RemoteProcessDetail.self, """
        {"node_id":"\(node)","key":"0a.Z28","name":"go","cmd":"go test","repo":"towertail",
         "from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"10s",
         "points":[{"ts":"2026-04-20T11:59:50Z","cpu_pct":50,"rss":2048,"io_bps":1000}],
         "facts":{"seen_at":"2026-04-20T11:59:50Z","pid":7,"ppid":1,"parent":"zsh","user":"root","cwd":"/src","state":"R","threads":4,
                  "start_ts":"2026-04-20T11:00:00Z","listen_tcp":[8080],"listen_udp":[53]}}
        """)
        XCTAssertEqual(d.points.count, 1)
        XCTAssertEqual(d.points[0].ioBps, 1000)
        XCTAssertEqual(d.facts?.pid, 7)
        XCTAssertEqual(d.facts?.listenTCP, [8080])
        XCTAssertEqual(d.facts?.listenUDP, [53])
    }

    func testDecodeProcessDetailWithNoFacts() throws {
        let d = try decode(RemoteProcessDetail.self, """
        {"node_id":"\(node)","key":"0a.Z28","name":"go","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"1m",
         "points":[]}
        """)
        XCTAssertNil(d.facts)
        XCTAssertEqual(d.cmd, "")
        XCTAssertTrue(d.points.isEmpty)
    }

    func testDecodeDiskInodes() throws {
        let r = try decode(RemoteDiskHistory.self, """
        {"mounts":[{"mount":"/","fs":"ext4","points":[
          {"ts":"2026-04-20T12:00:00Z","used":50,"total":100,"inodes_used":9,"inodes_total":10},
          {"ts":"2026-04-20T12:01:00Z","used":50,"total":100}]}]}
        """)
        XCTAssertEqual(r.mounts[0].points[0].inodesUsed, 9)
        XCTAssertEqual(r.mounts[0].points[0].inodesTotal, 10)
        XCTAssertNil(r.mounts[0].points[1].inodesUsed)
    }
}
