import XCTest
@testable import Towertail

@MainActor
final class DiskScanStoreTests: XCTestCase {
    private func tempURL() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("towertail-scan-\(UUID().uuidString).sqlite")
    }

    private func result(_ bytes: Int64) -> DiskScanResult {
        DiskScanResult(
            root: "/Users/dev", os: "darwin", hostname: "mac", startedTs: "2026-10-15T09:12:00Z",
            durationMs: 41_000, scannedBytes: bytes,
            tree: DiskNode(n: "/Users/dev", b: bytes, c: [DiskNode(n: "code", b: bytes, cat: .code, id: "x")])
        )
    }

    func testKeepsLastThreePerNodeAndScope() {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = DiskScanStore(url: url)
        let node = UUID()
        let other = UUID()
        let t0 = Date(timeIntervalSince1970: 1_790_000_000)
        for i in 0..<5 {
            store.save(nodeID: node, scope: "~", result: result(Int64(i + 1)), finishedAt: t0.addingTimeInterval(Double(i)))
        }
        store.save(nodeID: node, scope: "/", result: result(99), finishedAt: t0)
        store.save(nodeID: other, scope: "~", result: result(7), finishedAt: t0)

        let home = store.records(nodeID: node, scope: "~")
        XCTAssertEqual(home.map(\.result.scannedBytes), [5, 4, 3])
        XCTAssertEqual(home.first?.durationMs, 41_000)
        XCTAssertEqual(store.latest(nodeID: node, scope: "/")?.result.scannedBytes, 99)
        XCTAssertEqual(store.records(nodeID: node).count, 4)
        XCTAssertEqual(store.latest(nodeID: other)?.result, result(7))

        // A new store on the same file sees the same rows.
        let again = DiskScanStore(url: url)
        XCTAssertEqual(again.records(nodeID: node, scope: "~").count, 3)

        store.removeAll(nodeID: node)
        XCTAssertTrue(store.records(nodeID: node).isEmpty)
        XCTAssertEqual(store.records(nodeID: other).count, 1)
    }

    func testStoresFixtureResult() throws {
        let r = try ScanFixtures.result("linux")
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = DiskScanStore(url: url)
        let node = UUID()
        let rev = store.revision
        store.save(nodeID: node, scope: "/", result: r)
        XCTAssertGreaterThan(store.revision, rev)
        XCTAssertEqual(store.latest(nodeID: node)?.result, r)
    }

    func testReplaceLatestKeepsTimeAndHistory() {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = DiskScanStore(url: url)
        let node = UUID()
        let t0 = Date(timeIntervalSince1970: 1_790_000_000)
        store.save(nodeID: node, scope: "~", result: result(1), finishedAt: t0)
        store.save(nodeID: node, scope: "~", result: result(2), finishedAt: t0.addingTimeInterval(60))
        store.save(nodeID: node, scope: "/", result: result(3), finishedAt: t0.addingTimeInterval(120))

        store.replaceLatest(nodeID: node, scope: "~", result: result(9))
        let rows = store.records(nodeID: node, scope: "~")
        XCTAssertEqual(rows.map(\.result.scannedBytes), [9, 1])
        XCTAssertEqual(rows.first?.finishedAt, t0.addingTimeInterval(60))
        XCTAssertEqual(store.latest(nodeID: node, scope: "/")?.result.scannedBytes, 3)
    }
}
