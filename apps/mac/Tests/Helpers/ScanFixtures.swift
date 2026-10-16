import XCTest
@testable import Towertail

/// Golden scan fixtures of the pinned sampler release in dist/samplers/.
enum ScanFixtures {
    static var repoRoot: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
    }

    /// Throws XCTSkip when the fixture is missing.
    static func url(_ os: String) throws -> URL {
        let url = repoRoot.appendingPathComponent("dist/samplers/scan-\(os).ndjson")
        guard FileManager.default.fileExists(atPath: url.path) else {
            throw XCTSkip("missing \(url.path); run scripts/fetch-samplers.sh")
        }
        return url
    }

    static func lines(_ os: String) throws -> [Data] {
        let data = try Data(contentsOf: url(os))
        return data.split(separator: 0x0A).map { Data($0) }
    }

    static func result(_ os: String) throws -> DiskScanResult {
        guard let last = try lines(os).last,
              case .result(let r) = try DiskScanEvent.decodeLine(last) else {
            throw XCTSkip("fixture \(os) has no result line")
        }
        return r
    }

    /// The dist sampler for this Mac, when it has the scan command.
    static func samplerWithScan() throws -> URL {
        let url = repoRoot.appendingPathComponent(
            "dist/samplers/\(LocalSamplerInvoker.hostTripleDefault)/towertail-sampler"
        )
        guard FileManager.default.isExecutableFile(atPath: url.path) else {
            throw XCTSkip("missing \(url.path); run scripts/fetch-samplers.sh")
        }
        let p = Process()
        p.executableURL = url
        p.arguments = ["scan", "--help"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try p.run()
        p.waitUntilExit()
        guard p.terminationStatus == 0 else {
            throw XCTSkip("the dist sampler has no scan command")
        }
        return url
    }
}
