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
        for m in [Metric.net, .health] {
            XCTAssertNil(MetricHoverContent.make(vm: v, metric: m), "\(m)")
        }
    }

    // MARK: Disk

    private let mb: Int64 = 1_048_576

    private func diskVM(
        os: String, root: Bool = true, items: [ProcSample]?, disks: [DiskSample]? = nil,
        readBps: Int64 = 0, writeBps: Int64 = 0, at t: Date = Date()
    ) -> ServerViewModel {
        let v = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        let base = makeSample(
            cpuPct: 10, memFrac: 0.3, diskFrac: 0.46, t: t,
            procs: items.map { ProcList(root: root, topN: 20, total: $0.count, visible: $0.count, items: $0) }
        )
        let host = HostInfo(name: "h", os: os, arch: "arm64", kernel: "6", uptimeS: 1, sampler: "x", machineID: nil)
        v.ingest(Sample(
            v: 1, ts: t, host: host, cpu: base.cpu, mem: base.mem, swap: base.swap,
            disks: disks ?? base.disks,
            diskIO: DiskIOInfo(readBps: readBps, writeBps: writeBps, readCum: 0, writeCum: 0, devices: nil),
            net: base.net, procs: base.procs, errors: []
        ))
        return v
    }

    private func ioProc(_ pid: Int32, _ name: String, read: Int64?, write: Int64?) -> ProcSample {
        ProcSample(pid: pid, name: name, cmd: name, cpuPct: 1, rss: 1, readBps: read, writeBps: write)
    }

    func testDiskSupported() throws {
        XCTAssertTrue(MetricHoverContent.supports(.disk))
        let v = diskVM(os: "linux", items: [ioProc(1, "rsync", read: 10 * mb, write: 0)], readBps: 10 * mb)
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .disk))
        XCTAssertEqual(c.value, "46%")
        guard case .readWrite(let r, let w) = c.chart else { return XCTFail("no chart") }
        XCTAssertEqual(r.count, 1)
        XCTAssertEqual(w.count, 1)
        XCTAssertEqual(r.first?.v ?? 0, 10, accuracy: 0.01)
    }

    func testDiskMacOSHostShowsNote() throws {
        let v = diskVM(os: "darwin", items: [ioProc(1, "rsync", read: 10 * mb, write: 0)])
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .disk))
        XCTAssertEqual(c.procs, .note("macOS does not report disk I/O per process."))
        XCTAssertNil(c.footnote)
    }

    func testDiskLinuxNonRootFootnote() throws {
        let v = diskVM(os: "linux", root: false, items: [ioProc(1, "rsync", read: mb, write: 0)])
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .disk))
        XCTAssertEqual(c.footnote, "Other users need cap_sys_ptrace")
        let root = diskVM(os: "linux", root: true, items: [ioProc(1, "rsync", read: mb, write: 0)])
        XCTAssertNil(try XCTUnwrap(MetricHoverContent.make(vm: root, metric: .disk)).footnote)
    }

    func testDiskWindowsNonRootFootnote() throws {
        let v = diskVM(os: "windows", root: false, items: [ioProc(1, "robocopy", read: mb, write: 0)])
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .disk))
        XCTAssertEqual(c.footnote, "Other users need elevation")
    }

    func testDiskTopThreeByIO() throws {
        let items = [
            ioProc(1, "idle", read: 0, write: 0),
            ioProc(2, "rsync", read: 40 * mb, write: 2 * mb),
            ioProc(3, "postgres", read: 5 * mb, write: 20 * mb),
            ioProc(4, "tar", read: 0, write: 12 * mb),
            ioProc(5, "cat", read: 1 * mb, write: 0),
        ]
        let c = try XCTUnwrap(MetricHoverContent.make(vm: diskVM(os: "linux", items: items), metric: .disk))
        guard case .top(let lines, let others) = c.procs else { return XCTFail("no top") }
        XCTAssertEqual(lines.map(\.title), ["rsync", "postgres", "tar"])
        XCTAssertEqual(lines.first?.value, "R 40 · W 2.0")
        XCTAssertNil(others)
    }

    func testDiskNoIOGivesNote() throws {
        let items = [ioProc(1, "idle", read: 0, write: 0)]
        let c = try XCTUnwrap(MetricHoverContent.make(vm: diskVM(os: "linux", items: items), metric: .disk))
        XCTAssertEqual(c.procs, .note("No process disk I/O now."))
        let none = [ioProc(1, "x", read: nil, write: nil)]
        let c2 = try XCTUnwrap(MetricHoverContent.make(vm: diskVM(os: "linux", items: none), metric: .disk))
        XCTAssertEqual(c2.procs, .note("No process I/O data."))
    }

    func testDiskFreeFromFullestMount() throws {
        let disks = [
            DiskSample(mount: "/", fs: "ext4", used: 40 * gb, total: 100 * gb),
            DiskSample(mount: "/data", fs: "ext4", used: 900 * gb, total: 1000 * gb),
        ]
        let v = diskVM(os: "linux", items: nil, disks: disks)
        let c = try XCTUnwrap(MetricHoverContent.make(vm: v, metric: .disk))
        XCTAssertEqual(c.detail, "100.00 GB free of 1000.00 GB")
    }

    func testFolderAndParentLineOnCpuCard() throws {
        let p = ProcSample(
            pid: 7, name: "node", cmd: "node /x/vitest run", cpuPct: 100, rss: gb,
            cwd: "/Users/dev/code/sling/web", repo: "/Users/dev/code/sling", parent: "claude"
        )
        let c = try XCTUnwrap(MetricHoverContent.make(vm: vm(items: [p]), metric: .cpu))
        guard case .top(let lines, _) = c.procs else { return XCTFail("no top") }
        let second = try XCTUnwrap(lines.first?.second)
        XCTAssertEqual(second.path?.repoName, "sling")
        XCTAssertEqual(second.parent, "claude")
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
