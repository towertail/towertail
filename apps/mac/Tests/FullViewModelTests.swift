import XCTest
@testable import Towertail

@MainActor
final class FullViewModelTests: XCTestCase {
    func testDetailTabMapping() {
        let cases: [(Metric, NetSubTab, DiskSubTab, DetailTab?)] = [
            (.cpu, .processes, .capacity, .processes),
            (.mem, .processes, .capacity, .processes),
            (.net, .processes, .capacity, .processes),
            (.net, .ports, .capacity, .ports),
            (.disk, .processes, .capacity, .disks),
            (.disk, .processes, .usage, nil),
            (.health, .processes, .capacity, .health),
        ]
        for (metric, net, disk, expected) in cases {
            let model = FullViewModel(metric: metric)
            model.netSubTab = net
            model.diskSubTab = disk
            XCTAssertEqual(model.detailTab, expected, "\(metric) \(net) \(disk)")
        }
    }
}
