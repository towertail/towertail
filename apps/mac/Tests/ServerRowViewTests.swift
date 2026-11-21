import XCTest
@testable import Towertail

@MainActor
final class ServerRowViewTests: XCTestCase {
    private let oneMB: Int64 = 1_048_576

    private func makeVM(cpu: Double = 34, rx: Int64 = 22, tx: Int64 = 9) -> ServerViewModel {
        let vm = ServerViewModel(hostname: "web-1", dnsName: "web-1", osArch: "linux/arm64")
        vm.ingest(makeSample(cpuPct: cpu, memFrac: 0.58, diskFrac: 0.72, rxBps: rx * oneMB, txBps: tx * oneMB))
        return vm
    }

    func testMetricsOrderAndText() {
        let m = ServerRowView.metrics(for: makeVM())
        XCTAssertEqual(m.map(\.text), ["34%", "58%", "72%", "31 MB/s"])
        XCTAssertEqual(m[0].fraction, 0.34, accuracy: 0.0001)
    }

    func testMetricKinds() {
        XCTAssertEqual(ServerRowView.metrics(for: makeVM()).map(\.metric), [.cpu, .mem, .disk, .net])
    }

    func testMetricTintAtThresholds() {
        XCTAssertEqual(ServerRowView.metrics(for: makeVM(cpu: 91))[0].tint, .critical)
        XCTAssertEqual(ServerRowView.metrics(for: makeVM(cpu: 76))[0].tint, .warn)
        XCTAssertEqual(ServerRowView.metrics(for: makeVM(cpu: 34))[0].tint, .nominal)
    }

    func testNetSplitFractions() throws {
        let split = try XCTUnwrap(ServerRowView.metrics(for: makeVM())[3].split)
        XCTAssertEqual(split.rx, 22.0 / 31.0, accuracy: 0.0001)
        XCTAssertEqual(split.tx, 9.0 / 31.0, accuracy: 0.0001)
    }

    func testIdleNetHasEmptySplit() throws {
        let m = ServerRowView.metrics(for: makeVM(rx: 0, tx: 0))[3]
        XCTAssertEqual(m.text, "0.0 MB/s")
        XCTAssertEqual(m.split, NetSplit(rx: 0, tx: 0))
    }

    func testNoDataMetricsShowDash() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        XCTAssertEqual(ServerRowView.metrics(for: vm).prefix(3).map(\.text), ["—", "—", "—"])
    }

    func testAlertedNoData() {
        let vm = ServerViewModel(hostname: "db-1", dnsName: "db-1", osArch: "x")
        vm.everConnected = true
        let base = Date()
        vm.checkStale(now: base.addingTimeInterval(240), limit: 30)
        XCTAssertEqual(
            ServerRowView.state(for: vm, now: base.addingTimeInterval(240)),
            .noSample(text: "No sample 4m · alerted", alerted: true)
        )
    }

    func testOfflineNeverConnectedIsNotAlerted() {
        let vm = ServerViewModel(hostname: "h", dnsName: "h", osArch: "x")
        vm.markOffline(reason: "refused", at: Date())
        XCTAssertEqual(ServerRowView.state(for: vm, now: Date()), .noSample(text: "Not connected", alerted: false))
    }

    func testLiveHostIsLive() {
        XCTAssertEqual(ServerRowView.state(for: makeVM(), now: Date()), .live)
    }

    func testAccessibilityLabel() {
        let vm = makeVM()
        let label = ServerRowView.accessibilityLabel(
            name: "web-1", state: .live, metrics: ServerRowView.metrics(for: vm)
        )
        XCTAssertEqual(
            label,
            "web-1, CPU 34 percent, memory 58 percent, disk 72 percent, network 31 megabytes per second"
        )
    }
}
