import XCTest
@testable import Towertail

@MainActor
final class ProcessPanelModelTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_700_000_000)

    private func vm() -> ServerViewModel {
        ServerViewModel(id: UUID(), hostname: "h", dnsName: "h", osArch: "linux amd64")
    }

    private func snap(_ i: Int, _ items: [ProcSample]) -> ProcSeries.Snapshot {
        ProcSeries.Snapshot(t: t0.addingTimeInterval(Double(i) * 10), items: items)
    }

    private func node(cpu: Double, rss: Int64 = 1_000) -> ProcSample {
        ProcSample(pid: 10, name: "node", cmd: "node vitest", cpuPct: cpu, rss: rss)
    }

    private var other: ProcSample { ProcSample(pid: 2, name: "sshd", cpuPct: 1, rss: 500) }

    private var window: ClosedRange<Date> { t0.addingTimeInterval(-60)...t0.addingTimeInterval(600) }

    private let ref = ProcessRef(key: ProcessRef.key(name: "node", cmd: "node vitest"), pid: 10, name: "node", cmd: "node vitest")

    func testLocalSourceBuildsSeriesFromMatchingSnapshots() async throws {
        let vm = vm()
        vm.procs.append(snap(0, [node(cpu: 40, rss: 1_000), other]))
        vm.procs.append(snap(1, [other]))
        vm.procs.append(snap(2, [node(cpu: 60, rss: 3_000), other]))
        let d = try await LocalProcessSource(vm: vm).detail(of: ref, window: window)
        XCTAssertEqual(d.cpu.map(\.v), [40, 60])
        XCTAssertEqual(d.rss.map(\.v), [1_000, 3_000])
        XCTAssertEqual(d.cpu.map(\.t), [t0, t0.addingTimeInterval(20)])
    }

    func testLocalFactsFromNewestSample() async throws {
        let vm = vm()
        vm.procs.append(snap(0, [ProcSample(pid: 10, name: "node", cmd: "node vitest", user: "old", cpuPct: 40, rss: 1)]))
        let start = t0.addingTimeInterval(-3_600)
        vm.procs.append(snap(1, [ProcSample(
            pid: 11, ppid: 1, name: "node", cmd: "node vitest", user: "fritz", cpuPct: 50, rss: 2, threads: 9,
            startTS: start, cwd: "/work", repo: "/work/app", parent: "zsh"
        ), other]))
        vm.ports = PortList(root: false, collectedTS: t0, maxConn: 100, truncated: false, total: 2, items: [
            PortItem(pid: 11, name: "node", user: "fritz", listenTCP: [8080, 3000], listenUDP: [5353],
                     estOut: 0, estIn: 0, udpSockets: nil, topRemotePorts: nil),
            PortItem(pid: 2, name: "sshd", user: "root", listenTCP: [22], listenUDP: nil,
                     estOut: 0, estIn: 0, udpSockets: nil, topRemotePorts: nil),
        ])
        let f = try await LocalProcessSource(vm: vm).detail(of: ref, window: window).facts
        XCTAssertEqual(f.user, "fritz")
        XCTAssertEqual(f.ppid, 1)
        XCTAssertEqual(f.parent, "zsh")
        XCTAssertEqual(f.cwd, "/work")
        XCTAssertEqual(f.repo, "/work/app")
        XCTAssertEqual(f.started, start)
        XCTAssertEqual(f.threads, 9)
        XCTAssertEqual(f.ports, ["tcp 3000", "tcp 8080", "udp 5353"])
        XCTAssertEqual(f.factsAt, t0.addingTimeInterval(10))
    }

    func testProcessOutsideTheWindowGivesEmptySeries() async throws {
        let vm = vm()
        vm.procs.append(snap(0, [other]))
        let d = try await LocalProcessSource(vm: vm).detail(of: ref, window: window)
        XCTAssertTrue(d.cpu.isEmpty)
        XCTAssertEqual(d.facts, .empty)
    }

    private final class CountingSource: ProcessDetailSource {
        var calls = 0
        func detail(of ref: ProcessRef, window: ClosedRange<Date>) async throws -> ProcessDetail {
            calls += 1
            let pts = (0..<5).map { MetricPoint(t: window.lowerBound.addingTimeInterval(Double($0) * 10), v: Double($0)) }
            return ProcessDetail(facts: .empty, cpu: pts, rss: pts, io: [])
        }
    }

    func testValuesAtCursorUseLoadedSeries() async {
        let source = CountingSource()
        let model = ProcessPanelModel(ref: ref, source: source)
        await model.load(window: t0...t0.addingTimeInterval(100))
        XCTAssertEqual(model.values(at: t0.addingTimeInterval(21))?.cpuPct, 2)
        XCTAssertEqual(model.values(at: t0.addingTimeInterval(39))?.cpuPct, 4)
        XCTAssertEqual(model.values(at: t0)?.cpuPct, 0)
        XCTAssertEqual(source.calls, 1)
    }

    func testNotRunningAtCursor() async {
        let model = ProcessPanelModel(ref: ref, source: CountingSource())
        XCTAssertNil(model.values(at: t0))
        await model.load(window: t0...t0.addingTimeInterval(100))
        XCTAssertEqual(model.values(at: t0.addingTimeInterval(-100))?.running, false)
        XCTAssertEqual(model.values(at: t0.addingTimeInterval(300))?.running, false)
        XCTAssertEqual(model.values(at: t0.addingTimeInterval(5))?.running, true)
    }

    func testCPUStatsCountTimeOverFiftyPercent() async {
        let model = ProcessPanelModel(ref: ref, source: CountingSource())
        await model.load(window: t0...t0.addingTimeInterval(100))
        // Values 0...4 never reach 50 %.
        XCTAssertEqual(model.cpuStats?.highFor, 0)
        XCTAssertEqual(model.cpuStats?.avg, 2)
        XCTAssertEqual(model.cpuStats?.peak, 4)
    }
}

final class ProcessRefTests: XCTestCase {
    func testLocalKeyJoinsNameAndCommand() {
        let ref = ProcessRef.local(ProcSample(pid: 5, name: "node", cmd: "node a.js", cpuPct: 1, rss: 1))
        XCTAssertEqual(ref.key, "node\u{0}node a.js")
        XCTAssertEqual(ref.pid, 5)
        XCTAssertEqual(ref.cmd, "node a.js")
    }

    func testNilCommandGivesNameKey() {
        let ref = ProcessRef.local(ProcSample(pid: 5, name: "WindowServer", cpuPct: 1, rss: 1))
        XCTAssertEqual(ref.key, "WindowServer\u{0}")
        XCTAssertEqual(ref.cmd, "")
    }
}
