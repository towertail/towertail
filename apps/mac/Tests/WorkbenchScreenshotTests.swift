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

    private func window(
        _ node: Node, vm: ServerViewModel, tab: HostTab = .overview, prepare: @escaping (FullViewModel) -> Void
    ) -> some View {
        let store = ServerStore()
        store.register(vm)
        return FullViewWindow(context: FullViewContext(hostId: node.id, metric: .cpu), initialTab: tab, prepare: prepare)
            .environment(store)
            .environment(ServerSettings(url: settingsURL))
            .environment(ClientSettings(url: settingsURL))
            .environment(NodeStore(nodes: [node], url: settingsURL))
            .environment(SamplerUpdateCoordinator(manifest: nil))
    }

    func testProcessPanelRenders() throws {
        let node = Node.localMac()
        let vm = filledVM(node: node)
        let size = CGSize(width: 1180, height: 760)
        let nodeRef = ProcessRef(
            key: ProcessRef.key(name: "node", cmd: "node vitest"), pid: 4410, name: "node", cmd: "node vitest"
        )
        let orb = ProcessRef(
            key: ProcessRef.key(name: "OrbStack Helper", cmd: "OrbStack Helper --vm"), pid: 812,
            name: "OrbStack Helper", cmd: "OrbStack Helper --vm"
        )
        let gone = ProcessRef(key: ProcessRef.key(name: "ffmpeg", cmd: "ffmpeg -i in.mov"), pid: 99, name: "ffmpeg", cmd: "ffmpeg -i in.mov")
        try render(window(node, vm: vm) { $0.openProcess = nodeRef }, size: size, name: "panel-live")
        try render(window(node, vm: vm) { $0.openProcess = orb }, size: size, name: "panel-live-light", dark: false)
        try render(window(node, vm: vm) { $0.openProcess = nodeRef }, size: CGSize(width: 1180, height: 1250), name: "panel-full")
        try render(window(node, vm: vm) {
            $0.openProcess = nodeRef
            $0.pin(at: Date().addingTimeInterval(-50 * 60))
        }, size: size, name: "panel-pinned")
        try render(window(node, vm: vm) { $0.openProcess = gone }, size: size, name: "panel-not-running")
        try render(window(node, vm: vm, tab: .processes) { $0.openProcess = orb; $0.offenderSelected = orb }, size: size, name: "panel-processes-tab")
        try render(window(node, vm: vm, tab: .health) { $0.openProcess = nodeRef }, size: size, name: "panel-health-tab")
        try render(window(node, vm: vm) { _ in }, size: CGSize(width: 980, height: 640), name: "workbench-overview-narrow")
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent("panel-live.png").path))
    }

    // MARK: - Cloud host

    private static let iso = ISO8601DateFormatter()

    private func cloudLoader() -> FakeCloudLoader {
        let fake = FakeCloudLoader()
        let mb = 1_048_576.0
        fake.windowProvider = { name, window, step in
            let secs = max(Double(step.components.seconds), 10)
            let n = Int(window.upperBound.timeIntervalSince(window.lowerBound) / secs)
            return (0...n).map { i in
                let t = window.lowerBound.addingTimeInterval(Double(i) * secs)
                let w = sin(Double(i) / 40)
                let v: Double
                switch name {
                case "cpu": v = 40 + 30 * w + (i > n - 60 ? 12 : 0)
                case "mem": v = 0.62 + 0.08 * w
                case "load": v = 3 + 2 * w
                case "disk": v = 0.41 + Double(i) / Double(n) * 0.03
                case "net_rx": v = (3 + 2 * w) * mb
                case "net_tx": v = (1 + w) * mb
                case "disk_read": v = (20 + 18 * w) * mb
                case "disk_write": v = (8 + 6 * w) * mb
                case "procs": v = 640 + 20 * w
                case "zombies": v = 0
                case "pids": v = 0.12
                case "files": v = 0.2
                case "inodes": v = 0.05
                case "mem_pressure": v = 1
                default: v = 0
                }
                return (t, v)
            }
        }
        let rows = [
            #"{"key":"a1.b3JiU3RhY2s","pid":812,"user":"fritz","name":"OrbStack Helper","cmd":"OrbStack Helper --vm","cpu_pct":71.2,"rss":3300000000,"threads":40,"io_bps":3145728}"#,
            #"{"key":"a2.bm9kZQ","pid":4410,"user":"fritz","name":"node","cmd":"node vitest","cpu_pct":62.4,"rss":412000000,"threads":12,"io_bps":4194304}"#,
            #"{"key":"a3.V2luZG93U2VydmVy","pid":402,"user":"_windowserver","name":"WindowServer","cmd":"","cpu_pct":18.2,"rss":690000000,"threads":20}"#,
            #"{"key":"a4.cG9zdGdyZXM","pid":311,"user":"fritz","name":"postgres","cmd":"postgres: main","cpu_pct":9.5,"rss":900000000,"threads":8,"io_bps":8388608}"#,
        ]
        fake.snapshotProvider = { b in
            let snap = FakeCloudLoader.snapshot(ts: b.start.addingTimeInterval(b.tier == .raw ? 0 : 0), rows: rows)
            return snap
        }
        fake.offendersProvider = { window in
            let span = window.upperBound.timeIntervalSince(window.lowerBound)
            let step = Int((span / 60).rounded(.up))
            func series(_ base: Double, _ amp: Double) -> String {
                (0..<60).map { String(format: "%.1f", max(0, base + amp * sin(Double($0) / 5))) }.joined(separator: ",")
            }
            func rss(_ v: Double) -> String { (0..<60).map { _ in String(Int(v)) }.joined(separator: ",") }
            func one(_ key: String, _ name: String, _ cmd: String, _ avg: Double, _ max: Double, _ r: Double, repo: String? = nil) -> String {
                let repoPart = repo.map { #","repo":"\#($0)""# } ?? ""
                return #"{"key":"\#(key)","name":"\#(name)","cmd":"\#(cmd)"\#(repoPart),"cpu_pct_avg":\#(avg),"cpu_pct_max":\#(max),"rss_max":\#(Int(r)),"series":{"cpu":[\#(series(avg, avg / 2))],"rss":[\#(rss(r))],"io":[\#(series(2, 1))]}}"#
            }
            let list = [
                one("a1.b3JiU3RhY2s", "OrbStack Helper", "OrbStack Helper --vm", 55, 96, 3_300_000_000),
                one("a2.bm9kZQ", "node", "node vitest", 31, 88, 412_000_000, repo: "towertail"),
                one("a4.cG9zdGdyZXM", "postgres", "postgres: main", 9, 40, 900_000_000),
                one("a3.V2luZG93U2VydmVy", "WindowServer", "", 6, 22, 690_000_000),
            ].joined(separator: ",")
            let f = Self.iso
            return FakeCloudLoader.decode("""
            {"node_id":"\(FakeCloudLoader.node)","from":"\(f.string(from: window.lowerBound))","to":"\(f.string(from: window.upperBound))",
             "series_from":"\(f.string(from: window.lowerBound))","series_step_s":\(step),"processes":[\(list)]}
            """)
        }
        fake.detailProvider = { key, window, step in
            let secs = max(Double(step.components.seconds), 10)
            let n = min(Int(window.upperBound.timeIntervalSince(window.lowerBound) / secs), 400)
            let f = Self.iso
            let pts = (0...n).map { i -> String in
                let t = window.lowerBound.addingTimeInterval(Double(i) * secs)
                let cpu = 40 + 35 * sin(Double(i) / 12)
                return #"{"ts":"\#(f.string(from: t))","cpu_pct":\#(cpu),"rss":\#(400_000_000 + i * 200_000),"io_bps":\#(2_000_000 + i * 1000)}"#
            }.joined(separator: ",")
            let seen = f.string(from: window.upperBound)
            return FakeCloudLoader.decode("""
            {"node_id":"\(FakeCloudLoader.node)","key":"\(key)","name":"node","cmd":"node vitest","repo":"towertail",
             "from":"\(f.string(from: window.lowerBound))","to":"\(seen)","resolution":"\(secs)s","points":[\(pts)],
             "facts":{"seen_at":"\(seen)","pid":4410,"ppid":311,"parent":"zsh","user":"fritz","cwd":"/Users/fritz/towertail",
                      "state":"R","threads":12,"start_ts":"\(f.string(from: window.upperBound.addingTimeInterval(-5 * 3600)))",
                      "listen_tcp":[5173],"listen_udp":[]}}
            """)
        }
        return fake
    }

    private func cloudWindow(
        _ node: Node, vm: ServerViewModel, tab: HostTab = .overview, loader: FakeCloudLoader,
        prepare: @escaping (FullViewModel) -> Void = { _ in }
    ) -> some View {
        let store = ServerStore()
        store.register(vm)
        return FullViewWindow(
            context: FullViewContext(hostId: node.id, metric: .cpu), initialTab: tab, cloudLoader: loader, prepare: prepare
        )
        .environment(store)
        .environment(ServerSettings(url: settingsURL))
        .environment(ClientSettings(url: settingsURL))
        .environment(NodeStore(nodes: [node], url: settingsURL))
        .environment(SamplerUpdateCoordinator(manifest: nil))
    }

    func testCloudHostRenders() throws {
        let node = Node.localMac()
        let vm = filledVM(node: node)
        vm.liveSource = .cloud
        let size = CGSize(width: 1180, height: 760)
        let nodeRef = ProcessRef(key: "a2.bm9kZQ", pid: nil, name: "node", cmd: "node vitest")
        try render(cloudWindow(node, vm: vm, loader: cloudLoader()), size: size, name: "cloud-overview")
        try render(cloudWindow(node, vm: vm, tab: .processes, loader: cloudLoader()), size: size, name: "cloud-processes")
        try render(cloudWindow(node, vm: vm, tab: .health, loader: cloudLoader()), size: size, name: "cloud-health")
        try render(cloudWindow(node, vm: vm, tab: .disks, loader: cloudLoader()), size: size, name: "cloud-disks")
        try render(cloudWindow(node, vm: vm, loader: cloudLoader()) { $0.openProcess = nodeRef }, size: size, name: "cloud-panel")
        try render(cloudWindow(node, vm: vm, loader: cloudLoader()) {
            $0.range = .h24
            $0.pin(at: Date().addingTimeInterval(-3 * 3600))
        }, size: size, name: "cloud-pinned")
        let hang = cloudLoader()
        hang.hangsSnapshots = true
        try render(cloudWindow(node, vm: vm, loader: hang) {
            $0.range = .h24
            $0.pin(at: Date().addingTimeInterval(-3 * 3600))
        }, size: size, name: "cloud-loading")
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent("cloud-overview.png").path))
    }
}
