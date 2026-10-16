import XCTest
@testable import Towertail

/// LocalDiskScanner with fake sampler scripts, and with the real dist
/// sampler on a temp folder.
final class DiskScannerTests: XCTestCase {
    private var dir: URL!

    override func setUpWithError() throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-scan-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: dir)
    }

    private let node = Node.localMac()

    private func scanner(_ script: String, watchdog: TimeInterval = 60) throws -> LocalDiskScanner {
        let url = dir.appendingPathComponent("fake-sampler.sh")
        try script.write(to: url, atomically: true, encoding: .utf8)
        return LocalDiskScanner(
            binary: URL(fileURLWithPath: "/bin/bash"), leadingArguments: [url.path],
            watchdog: watchdog, cancelGrace: 2
        )
    }

    private func collect(_ s: some DiskScanner, request: DiskScanRequest = DiskScanRequest()) async -> ([DiskScanEvent], Error?) {
        var events: [DiskScanEvent] = []
        do {
            for try await e in s.scan(node: node, request: request) { events.append(e) }
            return (events, nil)
        } catch {
            return (events, error)
        }
    }

    func testStreamsFixtureLinesInOrder() async throws {
        let fixture = try ScanFixtures.url("darwin")
        let s = try scanner("echo \"$@\" > '\(dir.path)/args'\ncat '\(fixture.path)'\n")
        let (events, error) = await collect(s, request: DiskScanRequest(root: "/tmp/x", maxDepth: 3))
        XCTAssertNil(error)
        let expected = try ScanFixtures.lines("darwin").map(DiskScanEvent.decodeLine)
        XCTAssertEqual(events, expected)
        let args = try String(contentsOf: dir.appendingPathComponent("args"), encoding: .utf8)
        XCTAssertEqual(args, "scan --root /tmp/x --max-depth 3\n")
    }

    func testCancelClosesStdin() async throws {
        let marker = dir.appendingPathComponent("stdin-closed")
        let s = try scanner("""
        echo '{"t":"progress","v":1,"files":1}'
        while read -r _; do :; done
        touch '\(marker.path)'
        echo '{"t":"cancelled","v":1}'
        """)
        let first = expectation(description: "first progress")
        let task = Task {
            var n = 0
            do {
                for try await _ in s.scan(node: node, request: DiskScanRequest()) {
                    n += 1
                    if n == 1 { first.fulfill() }
                }
            } catch {}
            return n
        }
        await fulfillment(of: [first], timeout: 5)
        task.cancel()
        _ = await task.value
        try await waitFor { FileManager.default.fileExists(atPath: marker.path) }
    }

    func testCancelKillsProcessThatIgnoresStdin() async throws {
        let pidFile = dir.appendingPathComponent("pid")
        let s = try scanner("""
        echo $$ > '\(pidFile.path)'
        echo '{"t":"progress","v":1,"files":1}'
        exec sleep 30
        """)
        let first = expectation(description: "first progress")
        let task = Task {
            do {
                for try await _ in s.scan(node: node, request: DiskScanRequest()) { first.fulfill() }
            } catch {}
        }
        await fulfillment(of: [first], timeout: 5)
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        task.cancel()
        try await waitFor(timeout: 6) { kill(pid, 0) != 0 }
    }

    func testWatchdogStopsSilentScan() async throws {
        let s = try scanner("""
        echo '{"t":"progress","v":1,"files":1}'
        exec sleep 30
        """, watchdog: 0.5)
        let (events, error) = await collect(s)
        XCTAssertEqual(events.count, 1)
        XCTAssertEqual(error as? DiskScanError, .noOutput(seconds: 1))
    }

    func testNonZeroExitKeepsStderr() async throws {
        let s = try scanner("echo 'unknown command \"scan\"' >&2\nexit 2\n")
        let (events, error) = await collect(s)
        XCTAssertTrue(events.isEmpty)
        guard case .exited(let code, let stderr)? = error as? DiskScanError else {
            return XCTFail("error: \(String(describing: error))")
        }
        XCTAssertEqual(code, 2)
        XCTAssertTrue(stderr.contains("unknown command"))
    }

    func testErrorLineEndsStream() async throws {
        let s = try scanner("""
        echo '{"t":"error","v":1,"message":"lstat /nope: no such file or directory"}'
        exit 1
        """)
        let (events, error) = await collect(s)
        XCTAssertNil(error)
        XCTAssertEqual(events, [.error("lstat /nope: no such file or directory")])
    }

    func testMissingResultIsAnError() async throws {
        let s = try scanner("echo '{\"t\":\"progress\",\"v\":1}'\n")
        let (_, error) = await collect(s)
        XCTAssertNotNil(error)
    }

    func testBadLineIsAnError() async throws {
        let s = try scanner("echo 'hello'\nsleep 5\n")
        let (_, error) = await collect(s)
        XCTAssertNotNil(error)
    }

    func testRealSamplerScansTempFolder() async throws {
        let bin = try ScanFixtures.samplerWithScan()
        let sub = dir.appendingPathComponent("data/node_modules/pkg")
        try FileManager.default.createDirectory(at: sub, withIntermediateDirectories: true)
        try Data(repeating: 7, count: 300_000).write(to: sub.appendingPathComponent("blob.bin"))
        let s = LocalDiskScanner(binary: bin)
        let (events, error) = await collect(s, request: DiskScanRequest(root: dir.path, minBytes: 1))
        XCTAssertNil(error)
        guard case .result(let r)? = events.last else { return XCTFail("no result: \(events)") }
        XCTAssertGreaterThanOrEqual(r.scannedBytes, 300_000)
        XCTAssertEqual(r.os, "darwin")
        XCTAssertGreaterThanOrEqual(r.files, 1)
    }

    private func waitFor(timeout: TimeInterval = 5, _ cond: @escaping () -> Bool) async throws {
        let end = Date().addingTimeInterval(timeout)
        while !cond() {
            if Date() > end { return XCTFail("condition not met in \(timeout) s") }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
    }
}
