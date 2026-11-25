import XCTest
@testable import Towertail

@MainActor
final class WorkbenchSeriesTests: XCTestCase {
    private let to = Date(timeIntervalSince1970: 1_800_000_000)
    private var window: ClosedRange<Date> { to.addingTimeInterval(-86_400)...to }

    func testOneRequestPerTabAndWindow() async {
        let fake = FakeCloudLoader()
        let sut = WorkbenchSeries()
        let host = FakeCloudLoader.node
        await sut.load(host: host, tab: .overview, window: window, step: .seconds(60), loader: fake)
        await sut.load(host: host, tab: .overview, window: window, step: .seconds(60), loader: fake)
        XCTAssertEqual(fake.windowCalls.count, 1)
        await sut.load(host: host, tab: .health, window: window, step: .seconds(60), loader: fake)
        XCTAssertEqual(fake.windowCalls.count, 2)
        await sut.load(host: host, tab: .alerts, window: window, step: .seconds(60), loader: fake)
        XCTAssertEqual(fake.windowCalls.count, 2)
        let later = window.lowerBound.addingTimeInterval(60)...window.upperBound.addingTimeInterval(60)
        await sut.load(host: host, tab: .overview, window: later, step: .seconds(60), loader: fake)
        XCTAssertEqual(fake.windowCalls.count, 3)
        XCTAssertLessThanOrEqual(fake.windowCalls.map(\.metrics.count).max() ?? 0, 16)
    }

    func testLivePointsAppendAfterLastRestPoint() async {
        let fake = FakeCloudLoader()
        fake.windowSeries["cpu"] = [(to.addingTimeInterval(-20), 50), (to.addingTimeInterval(-10), 100)]
        let sut = WorkbenchSeries()
        await sut.load(host: FakeCloudLoader.node, tab: .overview, window: window, step: .seconds(10), loader: fake)
        XCTAssertEqual(sut.series["cpu"]?.map(\.v), [0.5, 1.0])
        var live = MetricSeries()
        live.append(MetricPoint(t: to.addingTimeInterval(-15), v: 0.9))
        live.append(MetricPoint(t: to.addingTimeInterval(-5), v: 0.2))
        let points = sut.points("cpu", live: live)
        XCTAssertEqual(points.map(\.v), [0.5, 1.0, 0.2])
    }

    func testErrorKeepsOldSeriesAndSetsMessage() async {
        let fake = FakeCloudLoader()
        fake.windowSeries["cpu"] = [(to, 10)]
        let sut = WorkbenchSeries()
        await sut.load(host: FakeCloudLoader.node, tab: .overview, window: window, step: .seconds(10), loader: fake)
        fake.failWindow = FakeCloudError.missing
        let next = window.lowerBound.addingTimeInterval(60)...window.upperBound.addingTimeInterval(60)
        await sut.load(host: FakeCloudLoader.node, tab: .overview, window: next, step: .seconds(10), loader: fake)
        XCTAssertNotNil(sut.error)
        XCTAssertEqual(sut.series["cpu"]?.count, 1)
    }

    func testFactsMapDetail() {
        let d: RemoteProcessDetail = FakeCloudLoader.decode("""
        {"node_id":"\(FakeCloudLoader.node)","key":"k","name":"go","repo":"towertail","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",
         "resolution":"10s","points":[],"facts":{"seen_at":"2026-04-20T11:59:50Z","pid":7,"user":"root","listen_tcp":[8080],"listen_udp":[53]}}
        """)
        let f = CloudProcessSource.facts(d)
        XCTAssertEqual(f.ports, ["tcp 8080", "udp 53"])
        XCTAssertEqual(f.repo, "towertail")
        XCTAssertEqual(f.user, "root")
    }

    func testDetailWithNoFactsKeepsRepo() async throws {
        let fake = FakeCloudLoader()
        fake.detail = FakeCloudLoader.decode("""
        {"node_id":"\(FakeCloudLoader.node)","key":"k","name":"go","repo":"towertail","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",
         "resolution":"1m","points":[{"ts":"2026-04-20T11:59:00Z","cpu_pct":5,"rss":100}]}
        """)
        let src = CloudProcessSource(host: FakeCloudLoader.node, loader: fake)
        let ref = ProcessRef(key: "k", pid: nil, name: "go", cmd: "")
        let d = try await src.detail(of: ref, window: window)
        XCTAssertEqual(d.facts.repo, "towertail")
        XCTAssertEqual(d.cpu.count, 1)
        XCTAssertTrue(d.io.isEmpty)
        XCTAssertEqual(fake.processCalls, ["k"])
    }
}
