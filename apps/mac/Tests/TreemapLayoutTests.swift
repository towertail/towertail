import XCTest
@testable import Towertail

final class TreemapLayoutTests: XCTestCase {
    private let rect = CGRect(x: 10, y: 20, width: 804, height: 470)

    private func check(_ values: [Double], file: StaticString = #filePath, line: UInt = #line) {
        let items = values.enumerated().map { TreemapLayout.Item(id: $0.offset, value: $0.element) }
        let placed = TreemapLayout.squarify(items, in: rect)
        let positive = values.filter { $0 > 0 }
        XCTAssertEqual(placed.count, positive.count, file: file, line: line)
        let total = positive.reduce(0, +)
        let area = Double(rect.width * rect.height)
        for p in placed {
            let want = values[p.id] / total * area
            let got = Double(p.rect.width * p.rect.height)
            XCTAssertEqual(got, want, accuracy: max(want * 0.01, 0.5), "area of \(p.id)", file: file, line: line)
            XCTAssertTrue(rect.insetBy(dx: -0.01, dy: -0.01).contains(p.rect), "\(p.rect) outside", file: file, line: line)
        }
        for i in placed.indices {
            for j in placed.indices where j > i {
                let o = placed[i].rect.intersection(placed[j].rect)
                XCTAssertTrue(o.isNull || o.width * o.height < 0.01, "\(placed[i].id) overlaps \(placed[j].id)", file: file, line: line)
            }
        }
        let sum = placed.reduce(0.0) { $0 + Double($1.rect.width * $1.rect.height) }
        if !placed.isEmpty { XCTAssertEqual(sum, area, accuracy: area * 0.001, file: file, line: line) }
    }

    func testAreasBoundsAndOverlap() {
        check([41.5, 17.1, 16.7, 13.4, 12, 9, 7.3, 6.6, 6.2, 5.1, 4.3, 2.8, 5])
        check([58, 47, 31, 22, 14, 6.5, 3.5])
        check([1, 1, 1, 1, 1, 1, 1])
        check([1_000_000, 1, 1])
        check((1...60).map { Double($0 * $0) })
    }

    func testEmptyAndZero() {
        XCTAssertTrue(TreemapLayout.squarify([], in: rect).isEmpty)
        XCTAssertTrue(TreemapLayout.squarify([.init(id: 0, value: 0)], in: rect).isEmpty)
        XCTAssertTrue(TreemapLayout.squarify([.init(id: 0, value: 5)], in: .zero).isEmpty)
        check([3, 0, -1, 2])
    }

    func testSingleItemFillsRect() {
        let p = TreemapLayout.squarify([.init(id: 7, value: 42)], in: rect)
        XCTAssertEqual(p.count, 1)
        XCTAssertEqual(p[0].id, 7)
        XCTAssertEqual(p[0].rect.minX, rect.minX, accuracy: 0.001)
        XCTAssertEqual(p[0].rect.width, rect.width, accuracy: 0.001)
        XCTAssertEqual(p[0].rect.height, rect.height, accuracy: 0.001)
    }

    func testTilesAspectIsReasonable() {
        let items = [41.5, 17.1, 16.7, 13.4, 12, 9].enumerated().map { TreemapLayout.Item(id: $0.offset, value: $0.element) }
        for p in TreemapLayout.squarify(items, in: rect) {
            let aspect = max(p.rect.width / p.rect.height, p.rect.height / p.rect.width)
            XCTAssertLessThan(aspect, 4)
        }
    }

    @MainActor
    func testFixtureTilesMarkSelection() throws {
        let r = try ScanFixtures.result("darwin")
        guard let rec = r.recs.first, let id = rec.nodeIDs.first else { throw XCTSkip("no rec") }
        let tiles = TreemapView.tiles(nodes: r.tree.children, selectedIDs: [id], size: CGSize(width: 804, height: 470))
        XCTAssertEqual(tiles.count, r.tree.children.filter { $0.bytes > 0 }.count)
        let marked = tiles.contains { $0.selected || $0.kids.contains { $0.selected } }
        XCTAssertTrue(marked)
    }
}
