import XCTest
@testable import Towertail

final class SampleDecodingTests: XCTestCase {
    func testDecodesCanonicalSample() throws {
        let json = """
        {
          "v": 1,
          "ts": "2026-04-20T12:00:00.000Z",
          "host": {
            "name": "mbp.local",
            "os": "darwin",
            "arch": "arm64",
            "kernel": "24.6.0",
            "uptime_s": 12345,
            "agent": "0.1.0+abc",
            "machine_id": "24c63e84-ed40-5734-bf08-72572b9bea7d"
          },
          "cpu": { "pct": 42.5, "load_1": 1.2, "load_5": 1.1, "load_15": 0.9, "cores": 10 },
          "mem":  { "used": 10485760000, "total": 34359738368 },
          "swap": { "used": 0, "total": 0 },
          "disks": [ { "mount": "/", "fs": "apfs", "used": 7e11, "total": 1e12 } ],
          "net":  { "rx_bps": 125000, "tx_bps": 35000, "rx_cum": 44143288101, "tx_cum": 38846186674 },
          "errors": []
        }
        """.data(using: .utf8)!

        let sample = try SampleCodec.decoder().decode(Sample.self, from: json)
        XCTAssertEqual(sample.v, 1)
        XCTAssertEqual(sample.host.name, "mbp.local")
        XCTAssertEqual(sample.host.os, "darwin")
        XCTAssertEqual(sample.host.arch, "arm64")
        XCTAssertEqual(sample.host.uptimeS, 12345)
        XCTAssertEqual(sample.host.machineID, "24c63e84-ed40-5734-bf08-72572b9bea7d")
        XCTAssertEqual(sample.cpu.pct, 42.5, accuracy: 0.0001)
        XCTAssertEqual(sample.cpu.cores, 10)
        XCTAssertEqual(sample.mem.total, 34359738368)
        XCTAssertEqual(sample.disks?.count, 1)
        XCTAssertEqual(sample.disks?.first?.mount, "/")
        XCTAssertEqual(sample.net?.rxBps, 125000)
        XCTAssertEqual(sample.errors, [])
    }

    func testDecodesNDJsonStream() throws {
        let line = """
        {"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6.6","uptime_s":1,"agent":"x"},"cpu":{"pct":10,"load_1":0,"load_5":0,"load_15":0,"cores":4},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}
        """
        let blob = ([line, line, line].joined(separator: "\n")).data(using: .utf8)!
        let decoder = SampleCodec.decoder()
        var decoded: [Sample] = []
        for rawLine in blob.split(separator: UInt8(ascii: "\n")) {
            let data = Data(rawLine)
            decoded.append(try decoder.decode(Sample.self, from: data))
        }
        XCTAssertEqual(decoded.count, 3)
        XCTAssertTrue(decoded.allSatisfy { $0.v == 1 })
        XCTAssertNil(decoded.first?.disks)
        XCTAssertNil(decoded.first?.net)
    }

    func testMissingMachineIDDecodes() throws {
        let json = """
        {"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6.6","uptime_s":1,"agent":"x"},"cpu":{"pct":10,"load_1":0,"load_5":0,"load_15":0,"cores":4},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}
        """.data(using: .utf8)!
        let sample = try SampleCodec.decoder().decode(Sample.self, from: json)
        XCTAssertNil(sample.host.machineID)
    }
}
