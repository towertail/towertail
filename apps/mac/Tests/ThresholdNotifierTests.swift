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
}
