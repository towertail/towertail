import XCTest
@testable import Towertail

final class UpdaterTests: XCTestCase {
    func testIsNewer() {
        XCTAssertTrue(Updater.isNewer("0.1.1", than: "0.1.0"))
        XCTAssertTrue(Updater.isNewer("0.2.0", than: "0.1.9"))
        XCTAssertTrue(Updater.isNewer("0.1.10", than: "0.1.9"))
        XCTAssertTrue(Updater.isNewer("0.1.1", than: "0.1.0-dev"))
        XCTAssertFalse(Updater.isNewer("0.1.0", than: "0.1.0"))
        XCTAssertFalse(Updater.isNewer("0.1.0", than: "0.1.0-dev"))
        XCTAssertFalse(Updater.isNewer("0.0.9", than: "0.1"))
    }

    func testDecodesRelease() throws {
        let json = """
        {"tag_name": "v0.1.4", "assets": [
          {"name": "Towertail.zip", "browser_download_url": "https://github.com/towertail/towertail/releases/download/v0.1.4/Towertail.zip"}
        ]}
        """
        let rel = try JSONDecoder().decode(Updater.Release.self, from: Data(json.utf8))
        XCTAssertEqual(rel.version, "0.1.4")
        XCTAssertEqual(rel.assets.first?.name, Updater.assetName)
    }
}
