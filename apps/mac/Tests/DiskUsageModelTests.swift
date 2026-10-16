import XCTest
@testable import Towertail

@MainActor
final class DiskUsageModelTests: XCTestCase {
    private var url: URL!
    private var store: DiskScanStore!
    private let node = Node.localMac()

    override func setUp() async throws {
        url = FileManager.default.temporaryDirectory.appendingPathComponent("tt-usage-\(UUID().uuidString).sqlite")
        store = DiskScanStore(url: url)
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: url)
    }

    private func waitFor(_ cond: @escaping @MainActor () -> Bool, timeout: TimeInterval = 3) async throws {
        let end = Date().addingTimeInterval(timeout)
        while !cond() {
            if Date() > end { return XCTFail("condition not met") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    func testIdleToRunningToDone() async throws {
        let r = try ScanFixtures.result("darwin")
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "~")
        XCTAssertEqual(m.phase, .idle)
        XCTAssertNil(m.record)

        m.start()
        XCTAssertTrue(m.isRunning)
        try await waitFor { fake.current != nil }
        XCTAssertEqual(fake.requestList, [DiskScanRequest()])
        fake.send(.progress(DiskScanProgress(files: 10, bytes: 500)))
        try await waitFor { m.progress?.files == 10 }

        fake.send(.result(r))
        try await waitFor { m.phase == .done }
        XCTAssertEqual(m.result, r)
        XCTAssertEqual(store.latest(nodeID: node.id, scope: "~")?.result, r)
        // Default selection: all safe recommendations.
        let safe = Set(r.recs.filter { $0.group == .safe }.map(\.id))
        XCTAssertEqual(m.selected, safe)
        XCTAssertEqual(m.selectedBytes, r.recs.filter { $0.group == .safe }.reduce(0) { $0 + $1.bytes })
    }

    func testRealLocalScanEndToEnd() async throws {
        let bin = try ScanFixtures.samplerWithScan()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-e2e-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = dir.appendingPathComponent(".npm/_cacache")
        try FileManager.default.createDirectory(at: cache, withIntermediateDirectories: true)
        try Data(repeating: 1, count: 2_000_000).write(to: cache.appendingPathComponent("blob"))
        let m = DiskUsageModel(node: node, store: store, scanner: LocalDiskScanner(binary: bin), scope: dir.path)
        m.start()
        try await waitFor({ m.phase != .running(nil) && !m.isRunning }, timeout: 30)
        XCTAssertEqual(m.phase, .done)
        XCTAssertGreaterThanOrEqual(m.result?.scannedBytes ?? 0, 2_000_000)
        XCTAssertEqual(store.latest(nodeID: node.id, scope: dir.path)?.result, m.result)
    }

    func testCancelReturnsToIdleAndCancelsStream() async throws {
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "/")
        m.start()
        try await waitFor { fake.current != nil }
        XCTAssertEqual(fake.requestList.first?.root, "/")
        m.cancel()
        XCTAssertEqual(m.phase, .idle)
        try await waitFor { fake.cancelCount == 1 }
        // A late event does not change the state.
        fake.send(.progress(DiskScanProgress(files: 1)))
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(m.phase, .idle)
    }

    func testFailureFromErrorLineAndThrow() async throws {
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "~")
        m.start()
        try await waitFor { fake.current != nil }
        fake.send(.error("lstat /x: permission denied"))
        fake.finish()
        try await waitFor { m.phase == .failed("lstat /x: permission denied") }

        m.start()
        try await waitFor { fake.requestList.count == 2 }
        fake.finish(throwing: DiskScanError.noOutput(seconds: 60))
        try await waitFor { m.phase == .failed("The sampler sent no data for 60 s.") }

        m.start()
        try await waitFor { fake.requestList.count == 3 }
        fake.finish()
        try await waitFor { m.phase == .failed("The scan stopped without a result.") }
    }

    func testSelectionAndScript() throws {
        let r = try ScanFixtures.result("linux")
        store.save(nodeID: node.id, scope: "/", result: r)
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "/")
        XCTAssertEqual(m.result, r)
        m.clearSelection()
        XCTAssertEqual(m.selectedBytes, 0)
        let review = try XCTUnwrap(r.recs.first { $0.group == .review })
        m.toggle(review.id)
        XCTAssertEqual(m.selectedBytes, review.bytes)
        XCTAssertEqual(m.selectedNodeIDs, Set(review.nodeIDs))
        XCTAssertTrue(m.script()?.text.contains(review.cmd.sh[0]) ?? false)
        m.toggle(review.id)
        XCTAssertTrue(m.selected.isEmpty)
        m.selectSafe()
        XCTAssertEqual(m.selected, m.defaultSelection)
    }

    func testExplorerFocusAndNoAccessTile() throws {
        let r = try ScanFixtures.result("linux")
        store.save(nodeID: node.id, scope: "/", result: r)
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "/")
        let est = try XCTUnwrap(r.noAccess.bytesEst)
        XCTAssertEqual(m.mapNodes.last?.cat, DiskCategory.none)
        XCTAssertEqual(m.mapTotal, r.tree.bytes + est)
        let folder = try XCTUnwrap(r.tree.children.first { !$0.children.isEmpty })
        m.open(folder.name)
        XCTAssertEqual(m.focus, [folder.name])
        XCTAssertEqual(m.mapNodes.map(\.name), folder.children.map(\.name))
        m.goUp(to: 0)
        XCTAssertEqual(m.focus, [])
        m.open("does-not-exist")
        XCTAssertEqual(m.focus, [])
    }

    func testStaleAndScopeChange() throws {
        let r = try ScanFixtures.result("darwin")
        var now = Date()
        store.save(nodeID: node.id, scope: "~", result: r, finishedAt: now)
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "~", now: { now })
        XCTAssertFalse(m.isStale)
        now = now.addingTimeInterval(25 * 3600)
        XCTAssertTrue(m.isStale)
        m.setScope("/")
        XCTAssertNil(m.record)
        m.setScope("~")
        XCTAssertNotNil(m.record)
    }

    func testAddRemoveAndToggleTiles() throws {
        let r = try ScanFixtures.result("darwin")
        store.save(nodeID: node.id, scope: "~", result: r)
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "~")
        let base = m.selectedBytes

        XCTAssertEqual(m.mark(["Movies"]), .none)
        m.add(["Movies"])
        XCTAssertEqual(m.mark(["Movies"]), .mine(on: true))
        XCTAssertEqual(m.mineItems.map(\.label), ["Movies/"])
        XCTAssertEqual(m.mineItems.first?.displayPath, "~/Movies")
        XCTAssertEqual(m.selectedBytes, base + 5_000_000_000)
        XCTAssertTrue(m.script()?.text.contains(#"rm -rf -- "$HOME"/'Movies'"#) ?? false)
        XCTAssertTrue(m.holdsSelection(["Movies"]))

        m.toggleTile(["Movies"])
        XCTAssertEqual(m.mark(["Movies"]), .mine(on: false))
        XCTAssertEqual(m.selectedBytes, base)
        XCTAssertEqual(m.mineItems.count, 1)

        // A parent takes in the items below it, and covers its tiles.
        m.add(["Projects", "api"])
        m.add(["Projects"])
        XCTAssertEqual(m.mineItems.map(\.displayPath), ["~/Movies", "~/Projects"])
        XCTAssertEqual(m.mark(["Projects", "web"]), .covered(by: "Projects"))
        m.add(["Projects", "web"])
        XCTAssertEqual(m.mineItems.count, 2)
        m.remove(["Projects"])
        XCTAssertEqual(m.mineItems.map(\.displayPath), ["~/Movies"])

        // A tile with a recommendation uses the recommendation.
        let installers = try XCTUnwrap(r.recs.first { $0.id == "installers" })
        XCTAssertEqual(m.mark(["Downloads"]), .rule(installers, on: m.selected.contains("installers")))
        m.remove(["Downloads"])
        XCTAssertTrue(m.isHidden(installers))
        XCTAssertEqual(m.hiddenRecs, [installers])
        XCTAssertFalse(m.visibleRecs.contains(installers))
        XCTAssertEqual(m.mark(["Downloads"]), .none)
        m.add(["Downloads"])
        XCTAssertFalse(m.isHidden(installers))
        XCTAssertTrue(m.selected.contains("installers"))
        XCTAssertEqual(m.mark(["Downloads", "Docker.dmg"]), .covered(by: "Downloads"))

        m.add(["Movies"])
        m.clearSelection()
        XCTAssertEqual(m.selectedCount, 0)
        XCTAssertEqual(m.mineItems.map(\.on), [false])
    }

    func testPicksStayAcrossRescanAndGoneItemsLeave() async throws {
        let r = try ScanFixtures.result("darwin")
        store.save(nodeID: node.id, scope: "~", result: r)
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "~")
        let go = try XCTUnwrap(r.recs.first { $0.id == "go-build-cache" })
        m.hide(go)
        m.add(["Movies"])
        m.add([".Trash"])

        var next = r
        next.tree.c?.removeAll { $0.name == ".Trash" }
        m.start()
        try await waitFor { fake.current != nil }
        fake.send(.result(next))
        try await waitFor { m.phase == .done }
        XCTAssertEqual(m.notice, "~/.Trash is gone, so it left the cleanup list.")
        XCTAssertEqual(m.mineItems.map(\.displayPath), ["~/Movies"])
        XCTAssertTrue(m.isHidden(go))
        XCTAssertFalse(m.selected.contains(go.id))

        let again = DiskUsageModel(node: node, store: store, scanner: fake, scope: "~")
        XCTAssertEqual(again.mineItems.map(\.displayPath), ["~/Movies"])
        XCTAssertTrue(again.isHidden(go))
        XCTAssertNil(again.notice)
        XCTAssertTrue(DiskUsageModel(node: node, store: store, scanner: fake, scope: "/").picks.isEmpty)
    }
}
