import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the long-running process UI to PNG files for a visual check.
/// Runs only when TT_SCREENSHOT_DIR is set.
@MainActor
final class LongProcScreenshotTests: XCTestCase {
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

    /// A host where "OrbStack Helper" and "node" stay high for 3 h.
    private func flaggedVM(node: Node) -> ServerViewModel {
        let vm = ServerViewModel(id: node.id, hostname: "mbp", dnsName: "fritz@mbp", osArch: "macOS")
        vm.everConnected = true
        var rule = LongProcRule.defaults
        rule.sustainSeconds = 1800
        vm.applyLongProc(node: node, global: rule)
        let now = Date()
        let start = now.addingTimeInterval(-3 * 86_400)
        for m in stride(from: -190, through: 0, by: 1) {
            let t = now.addingTimeInterval(Double(m * 60))
            let items = [
                ProcSample(pid: 812, name: "OrbStack Helper", user: "fritz", cpuPct: 85, rss: 3_300_000_000, threads: 40, startTS: start),
                ProcSample(pid: 4410, name: "node", user: "fritz", cpuPct: m > -41 ? 62.4 : 5, rss: 412_000_000, threads: 12,
                           startTS: now.addingTimeInterval(-5 * 3600)),
                ProcSample(pid: 402, name: "WindowServer", user: "_windowserver", cpuPct: 18.2, rss: 690_000_000, threads: 20,
                           startTS: start),
            ]
            vm.ingest(Sample(
                v: 1, ts: t,
                host: HostInfo(name: "mbp", os: "darwin", arch: "arm64", kernel: "24.6", uptimeS: 1, sampler: "x", machineID: nil),
                cpu: CPUInfo(pct: 64, load1: 0, load5: 0, load15: 0, cores: 10),
                mem: MemInfo(used: 13_000_000_000, total: 16_000_000_000),
                swap: MemInfo(used: 0, total: 0),
                disks: [DiskSample(mount: "/", fs: "apfs", used: 30, total: 100)],
                procs: ProcList(root: false, topN: 20, total: 3, visible: 3, items: items),
                health: HealthInfo(procs: 640, zombies: 0, memPressure: 1),
                errors: []
            ), now: t)
        }
        return vm
    }

    func testCardAndFullView() throws {
        let node = Node(displayName: "mbp", kind: .ssh, sshUser: "fritz", sshHost: "mbp")
        let vm = flaggedVM(node: node)
        XCTAssertEqual(vm.longProcs.count, 2)
        let nodes = NodeStore(nodes: [node], url: settingsURL)

        let card = ServerCardView(vm: vm)
            .padding(16)
            .environment(ClientSettings(url: settingsURL))
            .environment(ServerSettings(url: settingsURL))
            .environment(nodes)
            .environment(SamplerUpdateCoordinator(manifest: nil))
        try render(card, size: CGSize(width: 380, height: 260), name: "longproc-card")

        let selection = Binding<Set<Int32>>(get: { [812] }, set: { _ in })
        let full = VStack(alignment: .leading, spacing: 16) {
            HealthDetails(health: vm.health, thresholds: vm.thresholds, longProcs: vm.longProcs, hostId: node.id)
                .padding(.horizontal, 16)
            ProcessTable(
                series: vm.procs, effectiveAt: nil, available: true, isRootSampler: false,
                metric: .health, node: node, longProcs: vm.longProcs, onIgnore: { _, _ in },
                selection: selection
            )
            Spacer()
        }
        .padding(.vertical, 16)
        try render(full, size: CGSize(width: 900, height: 560), name: "longproc-fullview")
    }

    func testPreferences() throws {
        var rule = LongProcRule.defaults
        rule.ignore = ["postgres", "clickhouse-server"]
        var host = ["OrbStack Helper"]
        let form = Form {
            Section("Long-running processes") {
                LongProcRuleControls(rule: Binding(get: { rule }, set: { rule = $0 }))
                LabeledContent("Ignore on all hosts") {
                    TagEditor(tags: Binding(get: { rule.ignore }, set: { rule.ignore = $0 }), prompt: "Add…")
                }
            }
            Section("Server edit window") {
                Toggle("Use custom long-running rule", isOn: .constant(false))
                LabeledContent("Ignore on this host") {
                    TagEditor(tags: Binding(get: { host }, set: { host = $0 }), prompt: "Add…")
                }
            }
        }
        .formStyle(.grouped)
        try render(form, size: CGSize(width: 680, height: 520), name: "longproc-prefs")
    }
}
