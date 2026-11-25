import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the rules of a Cloud host to PNG files for a visual check.
/// Runs only when TT_SCREENSHOT_DIR is set.
@MainActor
final class CloudHostRulesScreenshotTests: XCTestCase {
    private let hostA = UUID(uuidString: "22222222-2222-2222-2222-222222222222")!
    private var dir: URL!
    private var tmp: URL!

    override func setUp() async throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        tmp = FileManager.default.temporaryDirectory.appendingPathComponent("tt-shot-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tmp, withIntermediateDirectories: true)
    }

    override func tearDown() async throws {
        if let tmp { try? FileManager.default.removeItem(at: tmp) }
    }

    private func makeRules() throws -> CloudRules {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "rules-view", withExtension: "json"))
        let cacheURL = tmp.appendingPathComponent("cloud-rules-\(UUID().uuidString).json")
        try FileManager.default.copyItem(at: url, to: cacheURL)
        let client = RemoteClient(config: .init(endpoint: URL(string: "http://127.0.0.1:9")!, token: "x"))
        return CloudRules(client: client, device: "test-mac", cache: CloudRulesCache(url: cacheURL))
    }

    private func me(_ role: String) throws -> CloudMe {
        try JSONDecoder().decode(CloudMe.self, from: Data(
            #"{"user":{"email":"fritz@example.com"},"org":{"name":"Towertail","plan":"team"},"role":"\#(role)"}"#.utf8))
    }

    private func render(dark: Bool, role: String, name: String) throws {
        let rules = try makeRules()
        let form = Form {
            CloudHostRulesForm(rules: rules, nodeID: hostA, me: try? me(role), hostOS: "linux")
        }
        .formStyle(.grouped)
        .frame(width: 620, height: 1500)
        .background(dark ? Color(.sRGB, red: 0x1C / 255, green: 0x1C / 255, blue: 0x1E / 255) : Color.white)
        .environment(\.colorScheme, dark ? .dark : .light)
        let host = NSHostingView(rootView: form)
        let rect = CGRect(x: 0, y: 0, width: 620, height: 1500)
        let window = NSWindow(contentRect: rect, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.appearance = appearance
        host.appearance = appearance
        window.contentView = host
        host.frame = rect
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.6))
        host.layoutSubtreeIfNeeded()
        let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        try XCTUnwrap(rep.representation(using: .png, properties: [:])).write(to: dir.appendingPathComponent("\(name).png"))
        window.close()
    }

    func testAdminRules() throws {
        try render(dark: false, role: "admin", name: "mac-rules-admin-light")
        try render(dark: true, role: "admin", name: "mac-rules-admin-dark")
    }

    func testReadOnlyRules() throws {
        try render(dark: false, role: "viewer", name: "mac-rules-viewer-light")
        try render(dark: true, role: "viewer", name: "mac-rules-viewer-dark")
    }
}
