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
        {"tag_name": "v0.1.4", "draft": false, "prerelease": false, "assets": [
          {"name": "Towertail.zip", "browser_download_url": "https://github.com/towertail/towertail/releases/download/v0.1.4/Towertail.zip"}
        ]}
        """
        let rel = try JSONDecoder().decode(Updater.Release.self, from: Data(json.utf8))
        XCTAssertEqual(rel.version, "0.1.4")
        XCTAssertEqual(rel.assets.first?.name, Updater.assetName)
    }

    func testPickSkipsServerAndIncompleteReleases() throws {
        let json = """
        [
          {"tag_name": "server/v0.1.0", "draft": false, "prerelease": false, "assets": [\(Self.zip("server/v0.1.0"))]},
          {"tag_name": "v0.3.0", "draft": false, "prerelease": false, "assets": []},
          {"tag_name": "v0.4.0", "draft": true, "prerelease": false, "assets": [\(Self.zip("v0.4.0"))]},
          {"tag_name": "v0.5.0-rc.1", "draft": false, "prerelease": true, "assets": [\(Self.zip("v0.5.0-rc.1"))]},
          {"tag_name": "v0.1.9", "draft": false, "prerelease": false, "assets": [\(Self.zip("v0.1.9"))]},
          {"tag_name": "v0.2.0", "draft": false, "prerelease": false, "assets": [\(Self.zip("v0.2.0"))]}
        ]
        """
        let releases = try JSONDecoder().decode([Updater.Release].self, from: Data(json.utf8))
        let picked = try XCTUnwrap(Updater.pick(releases))
        XCTAssertEqual(picked.0.tagName, "v0.2.0")
        XCTAssertEqual(picked.1.browserDownloadURL.lastPathComponent, Updater.assetName)
    }

    func testPickReturnsNilWithoutAppRelease() throws {
        let json = """
        [{"tag_name": "server/v0.1.0", "draft": false, "prerelease": false, "assets": [\(Self.zip("server/v0.1.0"))]}]
        """
        let releases = try JSONDecoder().decode([Updater.Release].self, from: Data(json.utf8))
        XCTAssertNil(Updater.pick(releases))
    }

    private static func zip(_ tag: String) -> String {
        #"{"name": "Towertail.zip", "browser_download_url": "https://github.com/towertail/towertail/releases/download/\#(tag)/Towertail.zip"}"#
    }
}
