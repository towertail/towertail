import XCTest
@testable import Towertail

@MainActor
final class MetricHoverCardTests: XCTestCase {
    private let gb: Int64 = 1_073_741_824

    private func proc(_ pid: Int32, _ name: String = "node", cpu: Double = 0, rss: Int64 = 0) -> ProcSample {
        ProcSample(pid: pid, name: name, cmd: "\(name) /p/node_modules/.bin/vitest run", cpuPct: cpu, rss: rss)
    }

    private func vm(
        cpu: Double = 91, mem: Double = 0.5, items: [ProcSample]? = nil, skipped: Bool = false,
        procsAt: Date? = nil, at t: Date = Date()
    ) -> ServerViewModel {
        let v = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let list = items.map {
            ProcList(root: true, topN: 20, total: skipped ? 4000 : $0.count, visible: $0.count,
                     skipped: skipped ? true : nil, items: skipped ? [] : $0)
        }
        v.ingest(makeSample(cpuPct: cpu, memFrac: mem, diskFrac: 0.1, t: procsAt == nil ? t : (procsAt ?? t), procs: list))
        if procsAt != nil {
            v.ingest(makeSample(cpuPct: cpu, memFrac: mem, diskFrac: 0.1, t: t))
        }
        return v
    }

    func testCpuContent() throws {
        let items = [proc(1, cpu: 100), proc(2, cpu: 90), proc(3, cpu: 70), proc(4, cpu: 50),
                     proc(5, "postgres", cpu: 40), proc(6, "redis", cpu: 10)]
        let c = try XCTUnwrap(MetricHoverContent.make(vm: vm(items: items), metric: .cpu))
        XCTAssertEqual(c.value, "91%")
        XCTAssertEqual(c.tint, .critical)
        XCTAssertEqual(c.detail, "Peak 91% · Avg 91%")
        guard case .top(let lines, let others) = c.procs else { return XCTFail("no top") }
        XCTAssertEqual(lines.map(\.label), ["node ×4 · vitest run", "postgres", "redis"])
        XCTAssertEqual(lines.map(\.value), ["310%", "40%", "10%"])
        // 91% of 4 cores = 364%; the lines explain 360%.
        XCTAssertEqual(others, "4%")
        guard case .percent(let points, _) = c.chart else { return XCTFail("no chart") }
        XCTAssertEqual(points.count, 1)
    }

    func testMemOthersIsRestOfBytes() throws {
        let items = [proc(1, cpu: 1, rss: gb), proc(2, "postgres", cpu: 1, rss: gb)]
        let c = try XCTUnwrap(MetricHoverContent.make(vm: vm(mem: 0.5, items: items), metric: .mem))
        guard case .top(let lines, let others) = c.procs else { return XCTFail("no top") }
        XCTAssertEqual(lines.count, 2)
        XCTAssertEqual(c.value, "50%")
        XCTAssertEqual(others, ProcessTable.formatBytes(8_000_000_000 - 2 * gb))
    }

    func testUnsupportedMetricsGiveNil() {
        let v = vm(items: [proc(1, cpu: 1)])
        for m in [Metric.disk, .net, .health] {
            XCTAssertNil(MetricHoverContent.make(vm: v, metric: m), "\(m)")
        }
    }

    func testNoDataGivesNil() {
        let v = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        XCTAssertNil(MetricHoverContent.make(vm: v, metric: .cpu))
    }

    func testNoProcsNote() throws {
        let c = try XCTUnwrap(MetricHoverContent.make(vm: vm(items: nil), metric: .cpu))
        XCTAssertEqual(c.procs, .note("No process data."))
    }

    func testStaleProcsNote() throws {
        let now = Date()
        let v = vm(items: [proc(1, cpu: 50)], procsAt: now.addingTimeInterval(-300), at: now)
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .cpu))
        XCTAssertEqual(c.procs, .note("No process data."))
    }

    func testSkippedNote() throws {
        let c = try XCTUnwrap(MetricHoverContent.make(vm: vm(items: [], skipped: true), metric: .cpu))
        XCTAssertEqual(c.procs, .note("Too many processes to list (4000)."))
    }

    func testOriginBelowThenAbove() {
        let bounds = CGSize(width: 360, height: 620)
        let card = CGSize(width: 300, height: 200)
        let below = MetricHoverOverlay.origin(anchor: CGRect(x: 200, y: 100, width: 46, height: 30), card: card, bounds: bounds)
        XCTAssertEqual(below.y, 136)
        let above = MetricHoverOverlay.origin(anchor: CGRect(x: 200, y: 500, width: 46, height: 30), card: card, bounds: bounds)
        XCTAssertEqual(above.y, 500 - 6 - 200)
    }

    func testOriginClampsX() {
        let bounds = CGSize(width: 360, height: 620)
        let card = CGSize(width: 300, height: 100)
        let right = MetricHoverOverlay.origin(anchor: CGRect(x: 300, y: 100, width: 46, height: 30), card: card, bounds: bounds)
        XCTAssertEqual(right.x, 360 - 8 - 300)
        let left = MetricHoverOverlay.origin(anchor: CGRect(x: 0, y: 100, width: 20, height: 30), card: card, bounds: bounds)
        XCTAssertEqual(left.x, 8)
    }

    private let target = MetricHoverTarget(hostId: UUID(), metric: .cpu, anchor: .zero)

    func testStateShowsAfterDelay() async throws {
        let state = MetricHoverState(delay: .milliseconds(10))
        state.enter(target)
        XCTAssertNil(state.shown)
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(state.shown, target)
    }

    func testExitBeforeDelayShowsNothing() async throws {
        let state = MetricHoverState(delay: .milliseconds(30))
        state.enter(target)
        state.exit(target)
        try await Task.sleep(nanoseconds: 80_000_000)
        XCTAssertNil(state.shown)
    }

    func testExitOfOtherTargetKeepsCard() {
        let state = MetricHoverState()
        state.show(target)
        state.exit(MetricHoverTarget(hostId: target.hostId, metric: .mem, anchor: .zero))
        state.exit(MetricHoverTarget(hostId: UUID(), metric: .cpu, anchor: .zero))
        XCTAssertEqual(state.shown, target)
    }

    func testEnterOfNextTargetBeforeExitOfPreviousStillShows() async throws {
        let state = MetricHoverState(delay: .milliseconds(10))
        let next = MetricHoverTarget(hostId: UUID(), metric: .cpu, anchor: .zero)
        state.show(target)
        state.enter(next)
        state.exit(target)
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(state.shown, next)
    }

    func testDismissClears() {
        let state = MetricHoverState()
        state.show(target)
        state.dismiss()
        XCTAssertNil(state.shown)
    }
}
