import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the five welcome steps to PNG. Runs only when TT_SCREENSHOT_DIR
/// is set (pass TEST_RUNNER_TT_SCREENSHOT_DIR to xcodebuild).
@MainActor
final class WelcomeScreenshotTests: XCTestCase {
    private var dir: URL!
    private var tmp: URL!
    private var server: LocalTestServer!

    private struct FakeDiscovery: ServerDiscovery {
        func tailscale() async throws -> [ServerCandidate] {
            [
                ServerCandidate(id: "ts:1", name: "web-1", address: "100.64.0.1", user: "ana", os: "linux", source: .tailscale),
                ServerCandidate(id: "ts:2", name: "web-2", address: "100.64.0.2", user: "ana", os: "linux", source: .tailscale),
                ServerCandidate(id: "ts:3", name: "db-1", address: "100.64.0.3", user: "ana", os: "linux", source: .tailscale),
                ServerCandidate(id: "ts:4", name: "build-mac", address: "100.64.0.4", user: "ana", os: "macOS", source: .tailscale),
            ]
        }
        func sshConfig() throws -> [ServerCandidate] {
            [ServerCandidate(id: "ssh:edge", name: "edge", address: "edge.example.com", user: "root", source: .sshConfig)]
        }
    }

    override func setUpWithError() throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        tmp = FileManager.default.temporaryDirectory.appendingPathComponent("tt-welcome-shot-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tmp, withIntermediateDirectories: true)
        server = try LocalTestServer(token: "shot")
        try server.start()
    }

    override func tearDown() async throws {
        server?.stop()
        if let tmp { try? FileManager.default.removeItem(at: tmp) }
    }

    private func render(_ flow: WelcomeFlow, name: String) throws {
        let host = NSHostingView(rootView: WelcomeContent(flow: flow))
        let rect = CGRect(origin: .zero, size: WelcomeContent.size)
        let window = NSWindow(contentRect: rect, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: .darkAqua)
        host.appearance = NSAppearance(named: .darkAqua)
        window.contentView = host
        host.frame = rect
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.5))
        host.layoutSubtreeIfNeeded()
        let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        let png = try XCTUnwrap(rep.representation(using: .png, properties: [:]))
        try png.write(to: dir.appendingPathComponent("\(name).png"))
        window.close()
    }

    func testRenderSteps() async throws {
        let url = tmp.appendingPathComponent("settings.json")
        let settings = ClientSettings(url: url)
        let nodes = NodeStore(nodes: [Node.localMac()], url: url)
        let serverSettings = ServerSettings(url: url)
        server.on("POST", "/v1/sampler/enroll") { req in
            let sent = (try? JSONSerialization.jsonObject(with: req.body) as? [String: Any]) ?? [:]
            let id = sent["node_id"] as? String ?? UUID().uuidString
            return .json(200, Data("{\"node_id\":\"\(id)\",\"sampler_token\":\"t\",\"endpoint\":\"https://api.example.com\"}".utf8))
        }
        let client = RemoteClient(config: .init(endpoint: server.baseURL, token: "shot"))
        let session = FakeSamplerSession()
        let silent = LockedBox<UUID?>(nil)
        let installer = CloudInstaller(
            client: { client }, nodes: nodes, settings: serverSettings,
            firstSample: { id, _ in id == silent.get() ? nil : "1.4.0+abc" },
            connect: { _ in session },
            upload: { _, _ in "/home/u/.towertail/bin/towertail-sampler" },
            binary: { _ in URL(fileURLWithPath: "/tmp/towertail-sampler") },
            clock: ImmediateClock()
        )
        let flow = WelcomeFlow(
            nodes: nodes, settings: settings, cloud: nil, installer: installer,
            discovery: FakeDiscovery(),
            probe: { c in
                switch c.id {
                case "ts:3": return (.okNoSystemd, "Linux")
                case "ssh:edge": return (.refused, nil)
                default: return (.ok(ms: 18 + c.name.count), "Linux")
                }
            },
            verify: { _ in }
        )
        flow.close = {}
        flow.email = "ana@acme.dev"

        try render(flow, name: "1-welcome")
        flow.next()
        await flow.loadSources()
        flow.selected = ["ts:1", "ts:2", "ts:3", "ts:4"]
        try render(flow, name: "2-servers")
        await flow.addSelected()
        silent.set(flow.added.first { $0.displayName == "db-1" }?.id)
        flow.next()
        try render(flow, name: "3-cloud")
        flow.next()
        try render(flow, name: "4-signin")
        flow.next()
        await flow.install()
        try render(flow, name: "5-install")

        for name in ["1-welcome", "2-servers", "3-cloud", "4-signin", "5-install"] {
            XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent("\(name).png").path))
        }
    }
}
