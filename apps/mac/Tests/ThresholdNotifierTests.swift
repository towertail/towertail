import XCTest
@testable import Towertail

@MainActor
final class ThresholdNotifierTests: XCTestCase {
    func testHealthBodyListsOnlyUnmutedReasons() {
        var t = MetricThresholds.defaults
        t.zombiesWarn = 1
        t.zombiesCritical = 1000
        let v = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x", thresholds: t)
        v.applyMuted(MutedSignals([.memPressure]))
        v.ingest(Sample(
            v: 1, ts: Date(),
            host: HostInfo(name: "h", os: "darwin", arch: "arm64", kernel: "24", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 10, load1: 0, load5: 0, load15: 0, cores: 4),
            mem: MemInfo(used: 1, total: 10),
            swap: MemInfo(used: 0, total: 0),
            health: HealthInfo(procs: 100, zombies: 5, memPressurePct: 85),
            errors: []
        ))
        let body = ThresholdNotifier.bodyText(vm: v, metric: .health)
        XCTAssertTrue(body.contains("zombies"), body)
        XCTAssertFalse(body.contains("Memory pressure"), body)
    }

    private func vmWithProcs(_ items: [ProcSample]) -> ServerViewModel {
        let v = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        v.ingest(makeSample(
            cpuPct: 90, memFrac: 0.8, diskFrac: 0.1,
            procs: ProcList(root: true, topN: 20, total: items.count, visible: items.count, items: items)
        ))
        return v
    }

    private func node(_ pid: Int32, cpu: Double, rss: Int64 = 0) -> ProcSample {
        ProcSample(pid: pid, name: "node", cmd: "node /p/node_modules/.bin/vitest run", cpuPct: cpu, rss: rss)
    }

    func testCpuBodyListsMergedCulprits() {
        let items = [node(1, cpu: 100), node(2, cpu: 90), node(3, cpu: 70), node(4, cpu: 50),
                     ProcSample(pid: 9, name: "postgres", cpuPct: 40, rss: 0)]
        let body = ThresholdNotifier.bodyText(vm: vmWithProcs(items), metric: .cpu)
        XCTAssertTrue(body.contains("Top: node ×4 · vitest run 310%, postgres 40%"), body)
    }

    func testMemBodyListsBytes() {
        let gb: Int64 = 1_073_741_824
        let items = [node(1, cpu: 1, rss: gb), node(2, cpu: 1, rss: gb / 2),
                     ProcSample(pid: 9, name: "postgres", cpuPct: 0, rss: gb / 4)]
        let body = ThresholdNotifier.bodyText(vm: vmWithProcs(items), metric: .mem)
        XCTAssertTrue(body.contains("Top: node ×2 · vitest run 1.50 GB, postgres 256.0 MB"), body)
    }

    private func longProc(name: String, cmd: String?) -> LongProc {
        LongProc(
            key: LongProcKey(ProcSample(pid: 812, name: name, cpuPct: 85, rss: 0)),
            pid: 812, name: name, cmd: cmd, cpuPct: 85,
            highSince: Date(timeIntervalSince1970: 0),
            startTS: Date(timeIntervalSince1970: 0)
        )
    }

    func testLongProcBodyUsesWhat() {
        let p = longProc(name: "node", cmd: "node /p/node_modules/.bin/vitest run")
        let now = Date(timeIntervalSince1970: 32 * 60)
        let body = ThresholdNotifier.longProcBody(p, now: now)
        XCTAssertTrue(body.hasPrefix("node · vitest run (pid 812) at 85% CPU for 32m."), body)
    }

    func testLongProcBodyWithoutCmdKeepsName() {
        let p = longProc(name: "OrbStack Helper", cmd: nil)
        let body = ThresholdNotifier.longProcBody(p, now: Date(timeIntervalSince1970: 32 * 60))
        XCTAssertTrue(body.hasPrefix("OrbStack Helper (pid 812) at 85% CPU"), body)
    }

    func testCPUBodyShowsFolderLabel() {
        let items = [
            ProcSample(pid: 1, name: "node", cmd: "node /p/node_modules/.bin/vitest run", cpuPct: 100, rss: 0,
                       cwd: "/w/.wt/SLING-40/sling/web", repo: "/w/.wt/SLING-40/sling"),
            ProcSample(pid: 2, name: "node", cmd: "node /p/node_modules/.bin/vitest run", cpuPct: 90, rss: 0,
                       cwd: "/w/.wt/SLING-40/sling/web", repo: "/w/.wt/SLING-40/sling"),
            ProcSample(pid: 3, name: "node", cmd: "node /p/x.js", cpuPct: 50, rss: 0, cwd: "/srv/api"),
        ]
        let body = ThresholdNotifier.bodyText(vm: vmWithProcs(items), metric: .cpu)
        XCTAssertTrue(body.contains("Top: node ×2 · sling (.wt/SLING-40) · vitest run 190%, node · api · x.js 50%"), body)
    }
}
