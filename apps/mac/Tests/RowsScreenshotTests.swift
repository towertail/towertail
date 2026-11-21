import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the Rows view of the popover to PNG. Runs only when
/// TT_SCREENSHOT_DIR is set (pass TEST_RUNNER_TT_SCREENSHOT_DIR to xcodebuild).
@MainActor
final class RowsScreenshotTests: XCTestCase {
    private var dir: URL!

    override func setUpWithError() throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    private func addHost(_ store: ServerStore, _ name: String, cpu: Double, mem: Double, disk: Double,
                         rx: Int64, tx: Int64, procs: ProcList? = nil, history: Bool = false) {
        let vm = ServerViewModel(hostname: name, dnsName: name, osArch: "linux/arm64")
        store.register(vm)
        let mb: Int64 = 1_048_576
        if history {
            // Samples of the last 15 minutes, so the sparkline has a shape.
            let now = Date()
            for i in stride(from: 59, through: 1, by: -1) {
                let load = 55 + 30 * sin(Double(i) / 6) + (i < 12 ? 10 : 0)
                vm.ingest(makeSample(cpuPct: load, memFrac: mem / 100, diskFrac: disk / 100, t: now.addingTimeInterval(-Double(i) * 15)))
            }
        }
        vm.ingest(makeSample(
            cpuPct: cpu, memFrac: mem / 100, diskFrac: disk / 100, rxBps: rx * mb, txBps: tx * mb, procs: procs
        ))
    }

    private func nodeProcs() -> ProcList {
        let cmd = "node /srv/app/node_modules/.bin/vitest run --coverage"
        let gb: Int64 = 1_073_741_824
        let folder = "/home/dev/code/sling/web"
        let repo = "/home/dev/code/sling"
        let items = [
            ProcSample(pid: 812, name: "node", cmd: cmd, cpuPct: 100, rss: gb, cwd: folder, repo: repo, parent: "claude"),
            ProcSample(pid: 813, name: "node", cmd: cmd, cpuPct: 90, rss: gb / 2, cwd: folder, repo: repo, parent: "claude"),
            ProcSample(pid: 814, name: "node", cmd: cmd, cpuPct: 70, rss: gb / 2, cwd: folder, repo: repo, parent: "claude"),
            ProcSample(pid: 815, name: "node", cmd: cmd, cpuPct: 50, rss: gb / 4, cwd: folder, repo: repo, parent: "claude"),
            ProcSample(pid: 901, name: "postgres", cmd: "postgres: main", cpuPct: 28, rss: gb),
            ProcSample(pid: 902, name: "nginx", cmd: "nginx: worker process", cpuPct: 12, rss: gb / 8),
            ProcSample(pid: 903, name: "redis-server", cmd: "redis-server *:6379", cpuPct: 6, rss: gb / 8),
        ]
        return ProcList(root: true, topN: 20, total: items.count, visible: items.count, items: items)
    }

    private func render(
        layout: PopoverLayout, name: String, withHistory: Bool = false, diskOS: String? = nil, hover: MetricHoverState = MetricHoverState(),
        showHover: ((ServerStore) -> MetricHoverTarget?)? = nil
    ) throws {
        let settingsURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-rows-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: settingsURL) }
        let settings = ClientSettings(url: settingsURL)
        settings.popoverLayout = layout
        let store = ServerStore()
        addHost(store, "This Mac", cpu: 18, mem: 64, disk: 51, rx: 3, tx: 1)
        addHost(store, "web-1", cpu: 34, mem: 58, disk: 72, rx: 22, tx: 9)
        addHost(store, "web-2", cpu: 27, mem: 61, disk: 44, rx: 14, tx: 6)
        if let diskOS {
            diskHost(store, os: diskOS)
        } else {
            addHost(store, "web-3", cpu: 91, mem: 70, disk: 46, rx: 38, tx: 21, procs: nodeProcs(), history: withHistory)
        }
        addHost(store, "api-1", cpu: 22, mem: 49, disk: 38, rx: 9, tx: 7)
        let down = ServerViewModel(hostname: "db-1", dnsName: "db-1", osArch: "linux/arm64")
        store.register(down)
        down.ingest(makeSample(cpuPct: 20, memFrac: 0.4, diskFrac: 0.4), now: Date().addingTimeInterval(-240))
        down.checkStale(now: Date().addingTimeInterval(240), limit: 30)
        addHost(store, "db-2", cpu: 41, mem: 77, disk: 83, rx: 12, tx: 4)
        addHost(store, "cache-1", cpu: 12, mem: 88, disk: 21, rx: 6, tx: 6)
        addHost(store, "edge-1", cpu: 9, mem: 31, disk: 96, rx: 41, tx: 30)

        if let target = showHover?(store) { hover.show(target) }

        let view = PopoverRoot(hover: hover)
            .environment(store)
            .environment(settings)
            .environment(NodeStore(nodes: [], url: settingsURL))
            .environment(\.scenePhase, .active)
            .environment(\.colorScheme, .dark)

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

    func testRowsPopover() throws {
        XCTAssertEqual(PopoverLayout.allCases.first, .rows)
        try render(layout: .rows, name: "popover-rows")
    }

    func testHoverCard() throws {
        try render(layout: .rows, name: "hover-cpu", withHistory: true) { store in
            let vm = store.serverVMs.first { $0.hostname == "web-3" }!
            // CPU column of the web-3 row (fourth row).
            return MetricHoverTarget(hostId: vm.id, metric: .cpu, anchor: CGRect(x: 128, y: 98, width: 54, height: 32))
        }
    }

    /// The web-3 host with disk I/O history and per-process I/O, for the given host OS.
    private func diskHost(_ store: ServerStore, os: String) {
        let vm = ServerViewModel(hostname: "web-3", dnsName: "web-3", osArch: "\(os)/arm64")
        store.register(vm)
        let mb: Int64 = 1_048_576
        let now = Date()
        let host = HostInfo(name: "web-3", os: os, arch: "arm64", kernel: "6", uptimeS: 1, sampler: "x", machineID: nil)
        let folder = "/home/dev/code/sling/web"
        let repo = "/home/dev/code/sling"
        func io(_ pid: Int32, _ name: String, _ r: Int64, _ w: Int64, parent: String? = nil, inRepo: Bool = true) -> ProcSample {
            ProcSample(pid: pid, name: name, cmd: name, cpuPct: 5, rss: mb,
                       cwd: inRepo ? folder : nil, repo: inRepo ? repo : nil, parent: parent,
                       readBps: r * mb, writeBps: w * mb)
        }
        let items = [
            io(21, "rsync", 40, 2, parent: "backup.sh", inRepo: false),
            io(22, "postgres", 5, 24, inRepo: false),
            io(23, "vitest", 3, 12, parent: "claude"),
        ]
        for i in stride(from: 59, through: 0, by: -1) {
            let t = now.addingTimeInterval(-Double(i) * 15)
            let r = Int64(10 + 30 * max(0, sin(Double(i) / 5)))
            let w = Int64(4 + 20 * max(0, cos(Double(i) / 7)))
            let base = makeSample(cpuPct: 20, memFrac: 0.5, diskFrac: 0.46, rxBps: 38 * mb, txBps: 21 * mb, t: t,
                                  procs: i == 0 ? ProcList(root: false, topN: 20, total: 3, visible: 3, items: items) : nil)
            vm.ingest(Sample(
                v: 1, ts: t, host: host, cpu: base.cpu, mem: base.mem, swap: base.swap, disks: base.disks,
                diskIO: DiskIOInfo(readBps: r * mb, writeBps: w * mb, readCum: 0, writeCum: 0, devices: nil),
                net: base.net, procs: base.procs, errors: []
            ))
        }
    }

    func testDiskHoverCard() throws {
        for os in ["linux", "darwin"] {
            try render(layout: .rows, name: "hover-disk-\(os)", diskOS: os) { store in
                let vm = store.serverVMs.first { $0.hostname == "web-3" }!
                // DISK column of the web-3 row.
                return MetricHoverTarget(hostId: vm.id, metric: .disk, anchor: CGRect(x: 238, y: 98, width: 54, height: 32))
            }
        }
    }
}
