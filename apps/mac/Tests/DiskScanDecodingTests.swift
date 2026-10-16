import XCTest
@testable import Towertail

final class DiskScanDecodingTests: XCTestCase {
    func testDecodesDarwinFixture() throws {
        let events = try ScanFixtures.lines("darwin").map(DiskScanEvent.decodeLine)
        XCTAssertGreaterThanOrEqual(events.count, 2)
        guard case .progress(let p) = events[1] else { return XCTFail("line 2 is not progress") }
        XCTAssertEqual(p.top.count, 5)
        XCTAssertEqual(p.usedBytes, 610_000_000_000)
        guard case .result(let r) = events.last else { return XCTFail("last line is not result") }
        XCTAssertEqual(r.os, "darwin")
        XCTAssertEqual(r.root, "/Users/dev")
        XCTAssertEqual(r.home, "/Users/dev")
        XCTAssertEqual(r.fs?.mount, "/System/Volumes/Data")
        XCTAssertNil(r.noAccess.bytesEst)
        XCTAssertEqual(r.noAccess.dirs, ["/Users/dev/Library/Mail"])
        XCTAssertEqual(r.tree.bytes, r.scannedBytes)
        XCTAssertEqual(r.tree.children.reduce(0) { $0 + $1.bytes }, r.tree.bytes)
        XCTAssertEqual(r.recs.first?.group, .safe)
        XCTAssertFalse(r.recs.contains { $0.cmd.sh.isEmpty })
        XCTAssertTrue(r.recs.allSatisfy { $0.cmd.pwsh.isEmpty })
        XCTAssertNotNil(r.startedAt)
        XCTAssertEqual(r.limits, DiskScanLimits(maxDepth: 6, minBytes: 50_000_000))
        XCTAssertTrue(r.refreshed.isEmpty)
    }

    func testDecodesLinuxFixture() throws {
        let r = try ScanFixtures.result("linux")
        XCTAssertEqual(r.os, "linux")
        XCTAssertEqual(r.root, "/")
        XCTAssertEqual(r.noAccess.bytesEst, 17_597_978_500)
        XCTAssertTrue(r.recs.contains { $0.elevated && $0.cmd.sh.first?.hasPrefix("sudo ") == true })
        let ids = r.tree.subtreeIDs
        for rec in r.recs {
            for id in rec.nodeIDs { XCTAssertTrue(ids.contains(id), "rec \(rec.id) points to missing node \(id)") }
        }
        // An "other" node is last among its siblings.
        func check(_ n: DiskNode) {
            if let i = n.children.firstIndex(where: { $0.isOther }) {
                XCTAssertEqual(i, n.children.count - 1)
            }
            n.children.forEach(check)
        }
        check(r.tree)
    }

    func testDecodesWindowsFixture() throws {
        let r = try ScanFixtures.result("windows")
        XCTAssertTrue(r.isWindows)
        XCTAssertEqual(r.root, "C:\\Users\\dev")
        XCTAssertTrue(r.recs.allSatisfy { !$0.cmd.pwsh.isEmpty && $0.cmd.sh.isEmpty })
        XCTAssertEqual(r.commands(for: r.recs[0]), r.recs[0].cmd.pwsh)
    }

    func testResultRoundTripsThroughJSON() throws {
        var r = try ScanFixtures.result("linux")
        r.refreshed = [DiskScanRefresh(path: "/var", at: Date(timeIntervalSince1970: 1_790_000_000))]
        let back = try JSONDecoder().decode(DiskScanResult.self, from: JSONEncoder().encode(r))
        XCTAssertEqual(back, r)
    }

    func testDecodesErrorAndCancelledLines() throws {
        let err = try DiskScanEvent.decodeLine(Data(#"{"t":"error","v":1,"message":"lstat /missing: no such file or directory"}"#.utf8))
        XCTAssertEqual(err, .error("lstat /missing: no such file or directory"))
        XCTAssertTrue(err.isFinal)
        let cancelled = try DiskScanEvent.decodeLine(Data(#"{"t":"cancelled","v":1}"#.utf8))
        XCTAssertEqual(cancelled, .cancelled)
        let progress = try DiskScanEvent.decodeLine(Data(#"{"t":"progress","v":1,"files":3}"#.utf8))
        guard case .progress(let p) = progress else { return XCTFail() }
        XCTAssertEqual(p.files, 3)
        XCTAssertEqual(p.top, [])
        XCTAssertFalse(progress.isFinal)
    }

    func testRejectsUnknownLineAndNewerVersion() {
        XCTAssertThrowsError(try DiskScanEvent.decodeLine(Data(#"{"t":"weird","v":1}"#.utf8)))
        XCTAssertThrowsError(try DiskScanEvent.decodeLine(Data(#"{"t":"progress","v":2}"#.utf8))) { err in
            XCTAssertEqual(err as? DiskScanError, .unsupportedVersion(2))
        }
        XCTAssertThrowsError(try DiskScanEvent.decodeLine(Data("not json".utf8)))
    }

    func testSampleLineMeansNoScanCommand() {
        let sample = Data(#"{"v":1,"ts":"2026-10-16T03:37:13.941Z","host":{"name":"mbp"}}"#.utf8)
        XCTAssertThrowsError(try DiskScanEvent.decodeLine(sample)) { err in
            XCTAssertEqual(err as? DiskScanError, .noScanCommand)
        }
    }

    func testRequestArgumentsAndQuoting() {
        XCTAssertEqual(DiskScanRequest().samplerArguments, ["scan"])
        XCTAssertEqual(DiskScanRequest().scope, "~")
        let r = DiskScanRequest(root: "/var/My 'data'", maxDepth: 4, minBytes: 1_000)
        XCTAssertEqual(r.samplerArguments, ["scan", "--root", "/var/My 'data'", "--max-depth", "4", "--min-bytes", "1000B"])
        XCTAssertEqual(
            r.shellCommand(binary: "~/.towertail/towertail-sampler"),
            #"~/.towertail/towertail-sampler scan --root '/var/My '\''data'\''' --max-depth 4 --min-bytes 1000B"#
        )
        XCTAssertEqual(DiskScanRequest(root: "/").shellCommand(binary: "s"), "s scan --root /")
        XCTAssertEqual(DiskScanRequest(root: "/a", minFrac: 0).samplerArguments, ["scan", "--root", "/a", "--min-frac", "0"])
        let refresh = try? JSONSerialization.jsonObject(
            with: JSONEncoder().encode(DiskScanRequest(root: "/a", minFrac: 0, refreshOf: "~"))
        ) as? [String: Any]
        XCTAssertEqual(refresh?["refresh_of"] as? String, "~")
        XCTAssertEqual(refresh?["min_frac"] as? Double, 0)
    }

    func testDecodesJob() throws {
        let json = """
        {"id":"j1","node_id":"n1","state":"running","root":"/","max_depth":6,"min_bytes":50000000,
         "progress":{"t":"progress","v":1,"files":412318,"dirs":30211,"bytes":90412000000,"no_access_dirs":7,
           "current":"/var/lib","elapsed_ms":37000,"used_bytes":195400000000,"top":[]},
         "error":"","cancel_requested":false,"created_ts":"2026-10-15T09:12:00Z",
         "started_ts":"2026-10-15T09:12:04.123Z","finished_ts":null,"result_bytes":0}
        """
        let job = try JSONDecoder().decode(DiskScanJob.self, from: Data(json.utf8))
        XCTAssertEqual(job.state, .running)
        XCTAssertEqual(job.progress?.files, 412_318)
        XCTAssertNotNil(job.createdTs)
        XCTAssertNotNil(job.startedTs)
        XCTAssertNil(job.finishedTs)
        XCTAssertFalse(job.state.isFinal)
        let back = try JSONDecoder().decode(DiskScanJob.self, from: JSONEncoder().encode(job))
        XCTAssertEqual(back, job)
    }

    func testSizeText() {
        XCTAssertEqual(DiskSize.text(13_400_000_000), "13.4 GB")
        XCTAssertEqual(DiskSize.text(151_000_000_000), "151 GB")
        XCTAssertEqual(DiskSize.text(300_000_000), "300 MB")
        XCTAssertEqual(DiskSize.count(2_900_000), "2.9M")
    }
}
