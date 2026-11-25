import XCTest
@testable import Towertail

@MainActor
final class ProcessSnapshotLoaderTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_800_000_005)
    private var clock: ManualClock!
    private var fake: FakeCloudLoader!
    private var sut: ProcessSnapshotLoader!

    override func setUp() {
        clock = ManualClock()
        fake = FakeCloudLoader()
        fake.clock = clock
        sut = ProcessSnapshotLoader(host: FakeCloudLoader.node, loader: fake, clock: clock)
    }

    private func time(_ ago: TimeInterval) -> Date { now.addingTimeInterval(-ago) }

    func testBurstSendsOneRequest() async {
        for i in 1...10 {
            sut.request(at: time(Double(i) * 100), by: .cpu, now: now)
            await settle()
            clock.advance(.milliseconds(20))
            await settle()
        }
        XCTAssertTrue(fake.snapshotCalls.isEmpty)
        clock.advance(.milliseconds(120))
        await settle()
        let target = ProcBucket.of(time(1000), now: now)
        XCTAssertEqual(fake.snapshotCalls.filter { $0 == target }.count, 1)
        XCTAssertEqual(fake.snapshotCalls.first, target)
        XCTAssertEqual(sut.bucket, target)
    }

    func testCacheHitSkipsDebounce() async {
        let b = ProcBucket.of(time(300), now: now)
        fake.cache[.init(bucket: b, by: .cpu)] = FakeCloudLoader.snapshot(ts: b.start)
        sut.request(at: time(300), by: .cpu, now: now)
        await settle()
        XCTAssertEqual(sut.bucket, b)
        XCTAssertTrue(sut.lastRequest?.hasPrefix("cache hit") == true)
        XCTAssertFalse(fake.snapshotCalls.contains(b))
    }

    func testNewerRequestCancelsOlder() async {
        let a = ProcBucket.of(time(300), now: now)
        let b = ProcBucket.of(time(900), now: now)
        sut.request(at: time(300), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(60))
        sut.request(at: time(900), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        XCTAssertFalse(fake.snapshotCalls.contains(a))
        XCTAssertEqual(fake.snapshotCalls.first, b)
        XCTAssertEqual(sut.bucket, b)
    }

    func testLoadingFlagOnlyAfter80ms() async {
        fake.latency = .milliseconds(50)
        sut.request(at: time(300), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        XCTAssertFalse(sut.showsLoading)
        clock.advance(.milliseconds(50))
        await settle()
        XCTAssertFalse(sut.showsLoading)
        XCTAssertNotNil(sut.snapshot)

        fake.latency = .milliseconds(200)
        sut.request(at: time(900), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        XCTAssertFalse(sut.showsLoading)
        clock.advance(.milliseconds(80))
        await settle()
        XCTAssertTrue(sut.showsLoading)
        clock.advance(.milliseconds(120))
        await settle()
        XCTAssertFalse(sut.showsLoading)
    }

    func testKeepsOldSnapshotWhileLoading() async {
        sut.request(at: time(300), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        let first = sut.snapshot?.ts
        XCTAssertNotNil(first)

        fake.latency = .milliseconds(500)
        sut.request(at: time(900), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        clock.advance(.milliseconds(80))
        await settle()
        XCTAssertTrue(sut.showsLoading)
        XCTAssertEqual(sut.snapshot?.ts, first)
    }

    func testPrefetchesTwoBucketsEachSideNotFuture() async {
        sut.request(at: time(1000), by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        let b = ProcBucket.of(time(1000), now: now)
        let starts = Set(fake.snapshotCalls.map(\.start))
        XCTAssertEqual(starts, Set([-2, -1, 0, 1, 2].map { b.shifted($0).start }))

        let newest = ProcessSnapshotLoader(host: FakeCloudLoader.node, loader: FakeCloudLoader(), clock: clock)
        let fake2 = FakeCloudLoader()
        let sut2 = ProcessSnapshotLoader(host: FakeCloudLoader.node, loader: fake2, clock: clock)
        _ = newest
        sut2.request(at: now, by: .cpu, now: now)
        await settle()
        clock.advance(.milliseconds(120))
        await settle()
        let n = ProcBucket.of(now, now: now)
        XCTAssertEqual(Set(fake2.snapshotCalls.map(\.start)), Set([-2, -1, 0].map { n.shifted($0).start }))
    }
}
