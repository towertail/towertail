import XCTest
@testable import Towertail

final class DiskScanMergeTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_790_000_000)
    private let xcode = ["Library", "Developer/Xcode"]

    private func sub(root: String, bytes: Int64, kids: [DiskNode] = [], recs: [DiskRec] = []) -> DiskScanResult {
        DiskScanResult(root: root, os: "darwin", hostname: "mac", home: "/Users/dev",
                       startedTs: "2026-10-16T09:00:00Z", scannedBytes: bytes,
                       tree: DiskNode(n: root, b: bytes, cat: .sdk, c: kids.isEmpty ? nil : kids), recs: recs)
    }

    private func rec(_ id: String, _ path: String, _ bytes: Int64, _ nodes: [String]) -> DiskRec {
        DiskRec(id: id, group: .safe, label: id, path: path, bytes: bytes, nodeIDs: nodes,
                cmd: DiskRecCmd(sh: ["rm -rf \(path)"]))
    }

    func testRefreshRequestUsesLimits() throws {
        let r = try ScanFixtures.result("darwin")
        let req = r.refreshRequest(for: xcode)
        XCTAssertEqual(req.root, "/Users/dev/Library/Developer/Xcode")
        XCTAssertEqual(req.maxDepth, 3)
        XCTAssertEqual(req.minBytes, 50_000_000)
        XCTAssertEqual(req.minFrac, 0)

        var old = r
        old.limits = nil
        XCTAssertEqual(old.refreshRequest(for: ["Movies"]).maxDepth, 5)
        XCTAssertEqual(old.refreshRequest(for: ["Movies"]).minBytes, 50_000_000)
    }

    func testPaths() throws {
        let r = try ScanFixtures.result("darwin")
        XCTAssertEqual(r.absolutePath(of: []), "/Users/dev")
        XCTAssertEqual(r.absolutePath(of: xcode), "/Users/dev/Library/Developer/Xcode")
        XCTAssertEqual(r.names(of: "/Users/dev/Library/Developer/Xcode"), xcode)
        XCTAssertEqual(r.names(of: "/Users/dev/Library/Developer/Xcode/DerivedData/Towertail-abc/Build"),
                       xcode + ["DerivedData/Towertail-abc/Build"])
        XCTAssertEqual(r.names(of: "/Users/dev"), [])
        XCTAssertNil(r.names(of: "/Users/dev/Library/Developer"))
        XCTAssertNil(r.names(of: "/Users/devx/Library"))
        XCTAssertEqual(r.displayPath("/Users/dev/Movies"), "~/Movies")

        let w = try ScanFixtures.result("windows")
        XCTAssertEqual(w.absolutePath(of: ["AppData\\Local", "Temp"]), "C:\\Users\\dev\\AppData\\Local\\Temp")
        XCTAssertEqual(w.names(of: "C:\\Users\\dev\\AppData\\Local\\Temp"), ["AppData\\Local", "Temp"])
        XCTAssertEqual(w.displayPath("C:\\Users\\dev\\AppData"), "%USERPROFILE%\\AppData")
        var drive = w
        drive.root = "C:\\"
        XCTAssertEqual(drive.absolutePath(of: ["Users"]), "C:\\Users")
    }

    func testMergeReplacesFolderAndMovesSizeUp() throws {
        let r = try ScanFixtures.result("darwin")
        let root = "/Users/dev/Library/Developer/Xcode"
        let s = sub(root: root, bytes: 1_500_000_000, kids: [
            DiskNode(n: "iOS DeviceSupport", b: 1_000_000_000, cat: .sdk, id: "n1"),
            DiskNode(n: "DerivedData", b: 500_000_000, cat: .other, id: "n2"),
        ], recs: [
            rec("xcode-device-support", "~/Library/Developer/Xcode/iOS DeviceSupport", 1_000_000_000, ["n1"]),
            rec("xcode-derived-data", "~/Library/Developer/Xcode/DerivedData", 500_000_000, ["n2"]),
        ])
        let m = try XCTUnwrap(r.merging(s, at: xcode, now: now))
        let delta: Int64 = 1_500_000_000 - 7_000_000_000

        XCTAssertEqual(m.tree.bytes, r.tree.bytes + delta)
        XCTAssertEqual(m.scannedBytes, r.scannedBytes + delta)
        XCTAssertEqual(m.files, r.files)
        let lib = try XCTUnwrap(m.tree.node(at: ["Library"]))
        XCTAssertEqual(lib.bytes, 29_760_000_000 + delta)
        XCTAssertEqual(lib.children.map(\.name), [
            "Containers/com.docker.docker/Data/vms/0", "Caches", "Developer/Xcode", "Application Support/Slack/Cache",
        ])
        let x = try XCTUnwrap(m.tree.node(at: xcode[...]))
        XCTAssertEqual(x.cat, .sdk)
        XCTAssertEqual(x.children.map(\.id), ["r1-n1", "r1-n2"])
        XCTAssertEqual(x.children.map(\.cat), [.sdk, .sdk])

        XCTAssertEqual(m.recs.count, r.recs.count)
        let dd = try XCTUnwrap(m.recs.first { $0.id == "xcode-derived-data" })
        XCTAssertEqual(dd.nodeIDs, ["r1-n2"])
        XCTAssertEqual(dd.bytes, 500_000_000)
        XCTAssertEqual(m.recs.first { $0.id == "go-build-cache" }, r.recs.first { $0.id == "go-build-cache" })
        XCTAssertEqual(m.refreshed, [DiskScanRefresh(path: root, at: now)])
        XCTAssertEqual(m.names(of: root + "/DerivedData"), xcode + ["DerivedData"])

        // A second refresh of the same folder gets new ids and one entry.
        let again = try XCTUnwrap(m.merging(s, at: xcode, now: now.addingTimeInterval(5)))
        XCTAssertEqual(again.tree.node(at: xcode[...])?.children.map(\.id), ["r2-n1", "r2-n2"])
        XCTAssertEqual(again.refreshed.count, 1)
        XCTAssertEqual(again.tree.bytes, m.tree.bytes)
    }

    func testMergeKeepsRecOfTheFolder() throws {
        let r = try ScanFixtures.result("darwin")
        let names = [".ollama/models/blobs"]
        let s = sub(root: "/Users/dev/.ollama/models/blobs", bytes: 6_000_000_000,
                    kids: [DiskNode(n: "sha256-9f43", b: 6_000_000_000, f: true)])
        let m = try XCTUnwrap(r.merging(s, at: names, now: now))
        let node = try XCTUnwrap(m.tree.node(at: names[...]))
        XCTAssertEqual(node.id, "n6")
        XCTAssertEqual(node.cat, .ai)
        XCTAssertEqual(node.children.first?.cat, .ai)
        let ollama = try XCTUnwrap(m.recs.first { $0.id == "ollama-models" })
        XCTAssertEqual(ollama.bytes, 6_000_000_000)
        XCTAssertEqual(ollama.nodeIDs, ["n6"])
    }

    func testMergeAdjustsRecsAboveAndBelow() throws {
        let tree = DiskNode(n: "/h", b: 100, c: [
            DiskNode(n: "a", b: 100, cat: .cache, id: "n1", c: [
                DiskNode(n: "b", b: 60, cat: .cache, c: [DiskNode(n: "x", b: 60, cat: .cache, id: "n2")]),
                DiskNode(n: "c", b: 40, cat: .cache, id: "n3"),
            ]),
        ])
        let r = DiskScanResult(root: "/h", os: "linux", hostname: "h", home: "/h", startedTs: "", scannedBytes: 100,
                               tree: tree, recs: [
                                   rec("all", "~/a", 100, ["n1"]),
                                   rec("two", "~/a/b/x, ~/a/c", 100, ["n2", "n3"]),
                                   rec("x", "~/a/b/x", 60, ["n2"]),
                                   rec("glob", "~/a/b/*", 60, []),
                                   rec("far", "~/z", 5, []),
                               ])
        let s = sub(root: "/h/a/b", bytes: 20, kids: [DiskNode(n: "x", b: 20, cat: .cache, id: "n1")],
                    recs: [rec("x", "~/a/b/x", 20, ["n1"])])
        let m = try XCTUnwrap(r.merging(s, at: ["a", "b"], now: now))
        XCTAssertEqual(m.tree.bytes, 60)
        XCTAssertEqual(m.tree.node(at: ["a"])?.children.map(\.name), ["c", "b"])
        XCTAssertEqual(m.recs.map(\.id), ["all", "two", "far", "x"])
        XCTAssertEqual(m.recs[0].bytes, 60)
        XCTAssertEqual(m.recs[1].nodeIDs, ["n3"])
        XCTAssertEqual(m.recs[1].bytes, 40)
        XCTAssertEqual(m.recs[3].nodeIDs, ["r1-n1"])
        XCTAssertNil(r.merging(s, at: ["a", "nope"], now: now))
    }

    func testRuleID() {
        XCTAssertEqual(rec("node-modules-2", "", 0, []).ruleID, "node-modules")
        XCTAssertEqual(rec("go-build-cache", "", 0, []).ruleID, "go-build-cache")
    }
}
