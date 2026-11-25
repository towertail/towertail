import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the "Move your rules to the cloud" sheet to PNG files for a
/// visual check. Runs only when TT_SCREENSHOT_DIR is set.
@MainActor
final class MoveRulesToCloudScreenshotTests: XCTestCase {
    private let token = "cloud-user-token"
    private var dir: URL!
    private var tmp: URL!
    private var server: LocalTestServer!
    private var client: RemoteClient!
    private var settings: ServerSettings!
    private var nodes: NodeStore!
    private var rules: CloudRules!

    override func setUp() async throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        tmp = FileManager.default.temporaryDirectory.appendingPathComponent("tt-shot-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tmp, withIntermediateDirectories: true)
        server = try LocalTestServer(token: token)
        try server.start()
        client = RemoteClient(config: .init(endpoint: server.baseURL, token: token))
        settings = ServerSettings(url: tmp.appendingPathComponent("settings.json"))
        rules = CloudRules(client: client, device: "fritz-mbp", cache: nil, gap: 0.05)
    }

    override func tearDown() async throws {
        server?.stop()
        if let tmp { try? FileManager.default.removeItem(at: tmp) }
    }

    /// The rows of the preview: org CPU, CPU timing, health limits, a host
    /// memory override, ignored processes and a long-running rule.
    private func makeMove() throws -> CloudRulesMove {
        var master = Node(displayName: "sling-master", kind: .ssh, sshUser: "u", sshHost: "m", cloud: CloudLink(enrolledAt: Date()))
        master.thresholdOverrides = ThresholdOverrides(mem: ThresholdPair(warn: 0.85, critical: 0.95))
        master.ignoredProcs = ["pg_dump"]
        var vm = Node(displayName: "vm", kind: .ssh, sshUser: "u", sshHost: "v", cloud: CloudLink(enrolledAt: Date()))
        vm.customLongProc = LongProcRule(enabled: true, cpuPct: 60, sustainSeconds: 600, notify: .all, ignore: [])
        let web = Node(displayName: "web-edge-1", kind: .ssh, sshUser: "u", sshHost: "w", cloud: CloudLink(enrolledAt: Date()))
        nodes = NodeStore(nodes: [master, vm, web], url: tmp.appendingPathComponent("nodes.json"))

        let cloudOrg = RemoteOrgRules(
            thresholds: {
                var t = MetricThresholds.defaults
                t.cpuWarn = 0.8
                t.cpuCritical = 0.9
                return t
            }(),
            alerts: {
                var a = AlertRules.defaults
                a.cpu = AlertRule(sustainSeconds: 0, notify: a.cpu.notify)
                return a
            }())
        settings.thresholds = cloudOrg.thresholds
        settings.thresholds.cpuWarn = 0.75
        for m in ThresholdMetric.health { settings.thresholds[m] = ThresholdPair(warn: settings.thresholds[m].warn * 0.9, critical: settings.thresholds[m].critical * 0.95) }
        settings.thresholds.health.macMemPressure = ThresholdPair(warn: 70, critical: 85)
        settings.alertRules = AlertRules.defaults
        settings.longProc = cloudOrg.longProc
        let view = RemoteRulesView(org: cloudOrg, hosts: [master, vm, web].map {
            .init(nodeId: $0.id, host: RemoteHostRules(), effective: RemoteEffectiveRules())
        })
        let data = try JSONEncoder().encode(view)
        server.on("GET", "/v1/rules") { _ in .json(200, data) }
        return CloudRulesMove(
            settings: settings, nodes: nodes, rules: { self.rules }, client: { self.client },
            isAdmin: { true }, device: "fritz-mbp", defaults: UserDefaults(suiteName: "tt-shot-\(UUID().uuidString)")!)
    }

    private func render(_ view: some View, size: CGSize, dark: Bool, name: String) throws {
        let root = view
            .frame(width: size.width, height: size.height)
            .background(dark ? Color(.sRGB, red: 0x1C / 255, green: 0x1C / 255, blue: 0x1E / 255) : Color(.sRGB, red: 0.96, green: 0.96, blue: 0.96))
            .environment(\.colorScheme, dark ? .dark : .light)
        let host = NSHostingView(rootView: root)
        let rect = CGRect(origin: .zero, size: size)
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

    private func sheet(_ move: CloudRulesMove) -> some View { MoveRulesToCloudSheet(move: move) }

    private func steps(dark: Bool) async throws {
        let tag = dark ? "dark" : "light"
        let size = CGSize(width: 760, height: 560)

        server.on("POST", "/v1/rules/import") { _ in
            Thread.sleep(forTimeInterval: 1.5)
            return .json(200, Data(#"{"backupId":"11111111-1111-1111-1111-111111111111","hosts":2,"org":true}"#.utf8))
        }
        let move = try makeMove()
        await move.open()
        XCTAssertEqual(move.rows.count, 6)
        try render(sheet(move), size: size, dark: dark, name: "mac-move-rules-compare-\(tag)")

        let sending = Task { await move.start() }
        try await Task.sleep(nanoseconds: 400_000_000)
        XCTAssertEqual(move.phase, .sending)
        try render(sheet(move), size: size, dark: dark, name: "mac-move-rules-confirm-\(tag)")
        await sending.value

        let toast = try XCTUnwrap(move.toast)
        try render(RulesMovedToast(text: toast).padding(.top, 30), size: CGSize(width: 760, height: 110), dark: dark,
                   name: "mac-move-rules-done-\(tag)")

        server.on("POST", "/v1/rules/import") { _ in .json(403, Data("{}".utf8)) }
        let failing = try makeMove()
        await failing.open()
        await failing.start()
        guard case .failed = failing.phase else { return XCTFail("expected a failure") }
        try render(sheet(failing), size: size, dark: dark, name: "mac-move-rules-error-\(tag)")
    }

    func testStepsLight() async throws { try await steps(dark: false) }
    func testStepsDark() async throws { try await steps(dark: true) }
}
