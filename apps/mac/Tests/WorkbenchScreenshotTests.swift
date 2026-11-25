import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the host workbench to PNG files for a visual check.
/// Runs only when TT_SCREENSHOT_DIR is set (pass TEST_RUNNER_TT_SCREENSHOT_DIR to xcodebuild).
@MainActor
final class WorkbenchScreenshotTests: XCTestCase {
    private var dir: URL!
    private var settingsURL: URL!

    override func setUp() async throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        settingsURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-wb-\(UUID().uuidString).json")
    }

    override func tearDown() async throws {
        if let settingsURL { try? FileManager.default.removeItem(at: settingsURL) }
    }

    private func render(_ view: some View, size: CGSize, name: String, dark: Bool = true) throws {
        let root = view
            .frame(width: size.width, height: size.height)
            .environment(\.colorScheme, dark ? .dark : .light)
            .environment(\.scenePhase, .active)
        let host = NSHostingView(rootView: root)
        let rect = CGRect(origin: .zero, size: size)
        let window = NSWindow(contentRect: rect, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        host.appearance = window.appearance
        window.contentView = host
        host.frame = rect
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(1.2))
        host.layoutSubtreeIfNeeded()
        let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        try XCTUnwrap(rep.representation(using: .png, properties: [:])).write(to: dir.appendingPathComponent("\(name).png"))
        window.close()
    }

    /// A host with 100 minutes of varied samples and a few busy processes.
    private func filledVM(node: Node) -> ServerViewModel {
        let vm = ServerViewModel(id: node.id, hostname: "mbp", dnsName: "fritz@mbp", osArch: "macOS arm64")
        vm.everConnected = true
        var rule = LongProcRule.defaults
        rule.sustainSeconds = 1800
        vm.applyLongProc(node: node, global: rule)
        let now = Date()
        let start = now.addingTimeInterval(-3 * 86_400)
        let mb: Int64 = 1_048_576
        for m in stride(from: -100, through: 0, by: 1) {
            let t = now.addingTimeInterval(Double(m * 60))
            let wave = sin(Double(m) / 9)
            let cpu = 45 + 30 * wave + (m > -25 ? 12 : 0)
            let items = [
                ProcSample(pid: 812, name: "OrbStack Helper", cmd: "OrbStack Helper --vm", user: "fritz", cpuPct: 55 + 25 * wave,
                           rss: 3_300_000_000, threads: 40, startTS: start, readBps: 2 * mb, writeBps: mb),
                ProcSample(pid: 4410, name: "node", cmd: "node vitest", user: "fritz", cpuPct: m > -41 ? 62.4 : 5,
                           rss: 412_000_000, threads: 12, startTS: now.addingTimeInterval(-5 * 3600), readBps: 0, writeBps: 4 * mb),
                ProcSample(pid: 402, name: "WindowServer", user: "_windowserver", cpuPct: 18.2, rss: 690_000_000,
                           threads: 20, startTS: start, readBps: 0, writeBps: 0),
                ProcSample(pid: 311, name: "postgres", cmd: "postgres: main", user: "fritz", cpuPct: 8 + 6 * wave,
                           rss: 900_000_000, threads: 8, startTS: start, readBps: 6 * mb, writeBps: 2 * mb),
            ]
            vm.ingest(Sample(
                v: 1, ts: t,
                host: HostInfo(name: "mbp", os: "darwin", arch: "arm64", kernel: "24.6.0", uptimeS: 360_000, sampler: "x", machineID: nil),
                cpu: CPUInfo(pct: cpu, load1: 2 + 3 * wave + 3, load5: 0, load15: 0, cores: 10),
                mem: MemInfo(used: Int64(10_000_000_000 + 2_000_000_000 * wave), total: 16_000_000_000),
                swap: MemInfo(used: 0, total: 0),
                disks: [
                    DiskSample(mount: "/", fs: "apfs", used: 400_000_000_000 + Int64(m) * 10_000_000, total: 1_000_000_000_000),
                    DiskSample(mount: "/Volumes/Data", fs: "apfs", used: 880_000_000_000, total: 1_000_000_000_000),
                ],
                diskIO: DiskIOInfo(
                    readBps: Int64(20 + 18 * wave) * mb, writeBps: Int64(8 + 6 * wave) * mb, readCum: 0, writeCum: 0, devices: nil
                ),
                net: NetInfo(rxBps: Int64(3 + 2 * wave) * mb, txBps: Int64(1 + wave) * mb, rxCum: 0, txCum: 0),
                procs: ProcList(root: false, topN: 20, total: items.count, visible: items.count, items: items),
                health: HealthInfo(procs: 640, zombies: 0, memPressure: 1),
                errors: []
            ), now: t)
        }
        return vm
    }

    func testEachTabRenders() throws {
        let node = Node.localMac()
        let vm = filledVM(node: node)
        let store = ServerStore()
        store.register(vm)
        let nodes = NodeStore(nodes: [node], url: settingsURL)
        let tabs: [(HostTab, String)] = [
            (.overview, "overview"), (.processes, "processes"), (.health, "health"),
            (.disks, "disks"), (.alerts, "alerts"), (.settings, "settings"),
        ]
        for (tab, name) in tabs {
            let view = FullViewWindow(context: FullViewContext(hostId: node.id, metric: .cpu), initialTab: tab)
                .environment(store)
                .environment(ServerSettings(url: settingsURL))
                .environment(ClientSettings(url: settingsURL))
                .environment(nodes)
                .environment(SamplerUpdateCoordinator(manifest: nil))
            try render(view, size: CGSize(width: 1180, height: 760), name: "workbench-\(name)")
            XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent("workbench-\(name).png").path))
        }
        let light = FullViewWindow(context: FullViewContext(hostId: node.id, metric: .cpu))
            .environment(store)
            .environment(ServerSettings(url: settingsURL))
            .environment(ClientSettings(url: settingsURL))
            .environment(nodes)
            .environment(SamplerUpdateCoordinator(manifest: nil))
        try render(light, size: CGSize(width: 1180, height: 760), name: "workbench-overview-light", dark: false)
    }
}
