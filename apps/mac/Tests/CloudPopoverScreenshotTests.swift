import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the popover with host tags and the footer to PNG. Runs only
/// when TT_SCREENSHOT_DIR is set (pass TEST_RUNNER_TT_SCREENSHOT_DIR to xcodebuild).
@available(macOS 14.0, *)
@MainActor
final class CloudPopoverScreenshotTests: XCTestCase {
    private let token = "shot-token"
    private var dir: URL!
    private var server: LocalTestServer!

    override func setUpWithError() throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        server = try LocalTestServer(token: token)
        try server.start()
    }

    override func tearDown() async throws {
        server?.stop()
        try await super.tearDown()
    }

    private func render(_ view: some View, name: String) throws {
        let host = NSHostingView(rootView: view)
        let rect = CGRect(x: 0, y: 0, width: 360, height: 620)
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

    func testSignedInPopover() async throws {
        let settingsURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-cloud-shot-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: settingsURL) }
        let link = CloudLink(enrolledAt: Date())
        let mac = Node.localMac()
        let web = Node(displayName: "web-1", kind: .ssh, sshUser: "u", sshHost: "w", cloud: link)
        let api = Node(displayName: "api-1", kind: .ssh, sshUser: "u", sshHost: "a", cloud: link)
        let db = Node(displayName: "db-1", kind: .ssh, sshUser: "u", sshHost: "d", cloud: link)
        let legacy = Node(displayName: "legacy-1", kind: .ssh, sshUser: "u", sshHost: "l")
        let all = [mac, web, api, db, legacy]
        let nodes = NodeStore(nodes: all, url: settingsURL)
        let store = ServerStore()
        for (i, n) in all.enumerated() {
            let vm = ServerViewModel(id: n.id, hostname: n.displayName, dnsName: n.userAtHost, osArch: "linux/arm64")
            store.register(vm)
            if n.id == db.id {
                vm.ingest(makeSample(cpuPct: 20, memFrac: 0.4, diskFrac: 0.4), now: Date().addingTimeInterval(-240))
                vm.checkStale(now: Date().addingTimeInterval(240), limit: 30)
            } else {
                let mb: Int64 = 1_048_576
                vm.ingest(makeSample(cpuPct: Double(18 + i * 9), memFrac: 0.5 + Double(i) / 20, diskFrac: 0.45 + Double(i) / 25,
                                     rxBps: Int64(3 + i * 4) * mb, txBps: Int64(1 + i * 2) * mb))
            }
        }

        let remote = try JSONEncoder().encode([web, api, db].map { $0.toRemote(global: .defaults) })
        server.on("GET", "/v1/me") { _ in
            .json(200, Data("""
            {"user":{"id":"u","email":"ana@acme.dev"},"org":{"id":"o","name":"Acme","plan":"solo"},"logins":[]}
            """.utf8))
        }
        server.on("GET", "/v1/billing") { _ in
            .json(200, Data("{\"plan\":{\"id\":\"solo\",\"maxNodes\":10,\"historyDays\":30},\"nodes\":3,\"plans\":[]}".utf8))
        }
        server.on("GET", "/v1/nodes") { _ in .json(200, remote) }
        let alert = """
        {"alerts":[{"id":"\(UUID().uuidString)","node_id":"\(db.id.uuidString)","kind":"reachability_changed","metric":"reachability","tint":"critical","ts":"2026-04-21T12:00:00Z","value":0}]}
        """
        server.on("GET", "/v1/alerts") { _ in .json(200, Data(alert.utf8)) }

        let keychain = FakeTokenKeychain()
        try keychain.set(token, account: "127.0.0.1:\(server.port)")
        let account = CloudAccount(endpoint: server.baseURL, keychain: keychain, client: CloudAuthClient(endpoint: server.baseURL))
        let cloud = CloudSession(account: account, servers: store, nodes: nodes)
        await account.restore()
        let deadline = Date().addingTimeInterval(5)
        while !server.hasWebSocketClient, Date() < deadline { try await Task.sleep(nanoseconds: 50_000_000) }
        server.sendToAllWebSockets(text: "{\"type\":\"hello\"}")
        while !(cloud.feed?.connected == true && cloud.feed?.alerted.contains(db.id) == true), Date() < deadline {
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        XCTAssertEqual(cloud.feed?.alerted, [db.id])

        for layout in [PopoverLayout.cards, .rows] {
            let settings = ClientSettings(url: settingsURL)
            settings.popoverLayout = layout
            let view = PopoverRoot()
                .environment(store)
                .environment(settings)
                .environment(nodes)
                .environment(ServerSettings(url: settingsURL))
                .environment(SamplerUpdateCoordinator(manifest: nil))
                .environment(cloud)
                .environment(\.scenePhase, .active)
                .environment(\.colorScheme, .dark)
            try render(view, name: "popover-cloud-\(layout.rawValue)")
        }
        cloud.feed?.stop()
    }
}
