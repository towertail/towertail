import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the mute bells and the macOS memory pressure number to PNG files
/// for a visual check. Runs only when TT_SCREENSHOT_DIR is set.
@MainActor
final class MuteScreenshotTests: XCTestCase {
    private var dir: URL!
    private var settingsURL: URL!

    override func setUp() async throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        settingsURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-shot-\(UUID().uuidString).json")
    }

    override func tearDown() async throws {
        if let settingsURL { try? FileManager.default.removeItem(at: settingsURL) }
    }

    private func render(_ view: some View, size: CGSize, name: String) throws {
        let root = view
            .frame(width: size.width, height: size.height)
            .background(Color(.sRGB, red: 0x1C / 255, green: 0x1C / 255, blue: 0x1E / 255))
            .environment(\.colorScheme, .dark)
        let host = NSHostingView(rootView: root)
        let rect = CGRect(origin: .zero, size: size)
        let window = NSWindow(contentRect: rect, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: .darkAqua)
        host.appearance = NSAppearance(named: .darkAqua)
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

    /// The mba case: override 98 / 99, 58 % in use, kernel level warn.
    private func mba() -> (Node, ServerViewModel) {
        let o = ThresholdOverrides(
            cpu: ThresholdPair(warn: 0.99, critical: 0.99),
            mem: ThresholdPair(warn: 0.90, critical: 0.95),
            disk: ThresholdPair(warn: 0.93, critical: 0.97),
            memPressure: ThresholdPair(warn: 98, critical: 99)
        )
        let node = Node(displayName: "mba", kind: .ssh, sshUser: "fritz", sshHost: "mba",
                        thresholdOverrides: o, mutedSignals: MutedSignals([.memPressure]))
        let vm = ServerViewModel(id: node.id, hostname: "mba", dnsName: "fritz@mba", osArch: "macOS · arm64",
                                 thresholds: .effective(global: .defaults, override: o))
        vm.everConnected = true
        vm.applyMuted(node.mutedSignals)
        vm.ingest(Sample(
            v: 1, ts: Date(),
            host: HostInfo(name: "mba", os: "darwin", arch: "arm64", kernel: "24.6", uptimeS: 1, sampler: "x", machineID: nil),
            cpu: CPUInfo(pct: 12, load1: 0, load5: 0, load15: 0, cores: 8),
            mem: MemInfo(used: 13_000_000_000, total: 16_000_000_000),
            swap: MemInfo(used: 0, total: 0),
            disks: [DiskSample(mount: "/", fs: "apfs", used: 40, total: 100)],
            health: HealthInfo(procs: 712, zombies: 0, pidsUsed: 712, pidsMax: 8000,
                               filesUsed: 9000, filesMax: 245_760, memPressure: 2, memPressurePct: 58),
            errors: []
        ))
        return (node, vm)
    }

    func testMuteUI() throws {
        let (node, vm) = mba()
        let nodes = NodeStore(nodes: [node], url: settingsURL)
        let store = ServerStore(nodeLookup: { _ in node })
        store.register(vm)
        let server = ServerSettings(url: settingsURL)
        server.mutedSignals = MutedSignals([.zombies])

        let detail = ServerDetailView(node: node, tester: ServerTester(), tab: .alerts)
            .environment(nodes)
            .environment(store)
            .environment(server)
            .environment(ClientSettings(url: settingsURL))
            .environment(SamplerUpdateCoordinator(manifest: nil))
        try render(detail, size: CGSize(width: 620, height: 900), name: "mute-host-alerts")

        let pane = AlertsPane(onShowServer: { _ in }, showMoreHealth: true)
            .environment(nodes)
            .environment(store)
            .environment(server)
        try render(pane, size: CGSize(width: 900, height: 1100), name: "mute-global-alerts")

        let full = HealthDetails(health: vm.health, thresholds: vm.thresholds, hostId: node.id)
            .padding(16)
        try render(full, size: CGSize(width: 620, height: 220), name: "mute-health-details")
    }
}
