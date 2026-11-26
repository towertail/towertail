import XCTest
@testable import Towertail

final class ByteFormatTests: XCTestCase {
    func testSize() {
        let cases: [(Int64, String)] = [
            (-5, "0 B"), (1023, "1023 B"), (1024, "1 KB"),
            (1_572_864, "1.5 MB"), (3_221_225_472, "3.00 GB"),
        ]
        for (bytes, text) in cases { XCTAssertEqual(ByteFormat.size(bytes), text) }
    }

    func testRate() {
        let cases: [(Double, String)] = [
            (-1, "0 B/s"), (500, "500 B/s"), (120_000, "117.2 KB/s"),
            (2_097_152, "2.0 MB/s"), (2_147_483_648, "2.00 GB/s"),
        ]
        for (bps, text) in cases { XCTAssertEqual(ByteFormat.rate(bytesPerSec: bps), text) }

        let ioCases: [(Double, String)] = [(-1, "—"), (0.7, "0 B/s"), (2048, "2.0 KB/s")]
        for (bps, text) in ioCases { XCTAssertEqual(ProcessTable.formatIORate(bps), text) }
    }
}
