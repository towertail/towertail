import XCTest
@testable import Towertail

@MainActor
final class FullViewModelTests: XCTestCase {
    private func model(_ metric: Metric = .cpu, source: LiveSource = .poll) -> FullViewModel {
        FullViewModel(context: FullViewContext(hostId: UUID(), metric: metric), source: source)
    }

    private let t0 = Date(timeIntervalSince1970: 1_700_000_000)

    func testContextMetricMapsToTabAndRail() {
        let cases: [(Metric, HostTab, RailMetric)] = [
            (.cpu, .overview, .cpu), (.mem, .overview, .mem), (.net, .overview, .net),
            (.disk, .disks, .disk), (.health, .health, .health),
        ]
        for (metric, tab, rail) in cases {
            let m = model(metric)
            XCTAssertEqual(m.tab, tab, "\(metric)")
            XCTAssertEqual(m.rail, rail, "\(metric)")
        }
    }

    func testDetailTabMapping() {
        let cases: [(HostTab, ProcessesView, DiskSubTab, DetailTab?)] = [
            (.overview, .offenders, .capacity, .processes),
            (.processes, .offenders, .capacity, .processes),
            (.processes, .ports, .capacity, .ports),
            (.health, .offenders, .capacity, .health),
            (.disks, .offenders, .capacity, .disks),
            (.disks, .offenders, .usage, nil),
            (.alerts, .offenders, .capacity, .processes),
            (.settings, .offenders, .capacity, .processes),
        ]
        for (tab, view, disk, expected) in cases {
            let m = model()
            m.tab = tab
            m.processesView = view
            m.diskSubTab = disk
            XCTAssertEqual(m.detailTab, expected, "\(tab) \(view) \(disk)")
        }
    }

    func testDefaultRangeFollowsSource() {
        XCTAssertEqual(model(source: .poll).range, .h1)
        XCTAssertEqual(model(source: .cloud).range, .h24)
    }

    func testDragOf120sOrMoreZooms() {
        let m = model()
        m.beginSelection(at: t0)
        m.updateSelection(to: t0.addingTimeInterval(120))
        m.endSelection()
        XCTAssertEqual(m.zoomRange, t0...t0.addingTimeInterval(120))
        XCTAssertNil(m.pendingSelection)
    }

    func testShortDragDoesNotZoom() {
        let m = model()
        m.beginSelection(at: t0)
        m.updateSelection(to: t0.addingTimeInterval(119))
        m.endSelection()
        XCTAssertNil(m.zoomRange)
        XCTAssertNil(m.pendingSelection)
    }

    func testEscapeUnpinsAndReturnsFalseWhenLive() {
        let m = model()
        XCTAssertFalse(m.handleEscape())
        m.pin(at: t0)
        XCTAssertTrue(m.handleEscape())
        XCTAssertTrue(m.isLive)
    }

    func testEscapeClosesProcessBeforeUnpin() {
        let m = model()
        m.pin(at: t0)
        m.openProcess = ProcessRef(key: "k", pid: 1, name: "n", cmd: "")
        XCTAssertTrue(m.handleEscape())
        XCTAssertNil(m.openProcess)
        XCTAssertFalse(m.isLive)
        XCTAssertTrue(m.handleEscape())
        XCTAssertTrue(m.isLive)
    }

    func testSpacePinsNewestThenReturnsToLive() {
        let m = model()
        m.pinLatest(t0)
        XCTAssertEqual(m.mode, .pinned(t0))
        m.pinLatest(t0)
        XCTAssertTrue(m.isLive)
    }

    func testStepPinClampsToWindowAndNow() {
        let m = model()
        let now = t0.addingTimeInterval(7_200)
        m.pin(at: now)
        XCTAssertTrue(m.stepPin(by: 1, span: 5, now: now))
        XCTAssertEqual(m.mode, .pinned(now))
        m.pin(at: now.addingTimeInterval(-3_598))
        XCTAssertTrue(m.stepPin(by: -1, span: 5, now: now))
        XCTAssertEqual(m.mode, .pinned(now.addingTimeInterval(-3_600)))
        XCTAssertTrue(m.stepPin(by: 1, span: 5, now: now))
        XCTAssertEqual(m.mode, .pinned(now.addingTimeInterval(-3_595)))
    }

    func testStepPinIgnoredWhenLive() {
        let m = model()
        XCTAssertFalse(m.stepPin(by: 1, span: 5, now: t0))
    }

    func testRangeChangeClearsZoomAndPin() {
        let m = model()
        m.zoomRange = t0...t0.addingTimeInterval(600)
        m.pin(at: t0)
        m.hoverAt = t0
        m.range = .h6
        XCTAssertNil(m.zoomRange)
        XCTAssertNil(m.hoverAt)
        XCTAssertTrue(m.isLive)
    }

    func testRailChangeClearsSortOverride() {
        let m = model()
        m.procSortOverride = .io
        XCTAssertEqual(m.procSort, .io)
        m.rail = .mem
        XCTAssertNil(m.procSortOverride)
        XCTAssertEqual(m.procSort, .rss)
    }

    func testRangeAvailabilityFollowsOldestData() {
        let m = model()
        let now = t0
        XCTAssertTrue(m.isAvailable(.h1, oldest: nil, now: now))
        XCTAssertFalse(m.isAvailable(.h6, oldest: nil, now: now))
        XCTAssertFalse(m.isAvailable(.h6, oldest: now.addingTimeInterval(-1_800), now: now))
        XCTAssertTrue(m.isAvailable(.h6, oldest: now.addingTimeInterval(-3_600), now: now))
        XCTAssertFalse(m.isAvailable(.h24, oldest: now.addingTimeInterval(-3_600), now: now))
    }
}
