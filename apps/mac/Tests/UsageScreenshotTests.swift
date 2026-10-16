import AppKit
import SwiftUI
import XCTest
@testable import Towertail

/// Renders the Usage sub-tab to PNG files for a visual check. Runs only
/// when TT_SCREENSHOT_DIR is set (pass TEST_RUNNER_TT_SCREENSHOT_DIR to
/// xcodebuild).
@MainActor
final class UsageScreenshotTests: XCTestCase {
    private var dir: URL!
    private var store: DiskScanStore!
    private var dbURL: URL!

    override func setUp() async throws {
        guard let path = ProcessInfo.processInfo.environment["TT_SCREENSHOT_DIR"], !path.isEmpty else {
            throw XCTSkip("TT_SCREENSHOT_DIR is not set")
        }
        dir = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        dbURL = FileManager.default.temporaryDirectory.appendingPathComponent("tt-shot-\(UUID().uuidString).sqlite")
        store = DiskScanStore(url: dbURL)
    }

    override func tearDown() async throws {
        if let dbURL { try? FileManager.default.removeItem(at: dbURL) }
    }

    func render(_ model: DiskUsageModel, vm: ServerViewModel, via: ScanVia, scopes: [String], name: String,
                allowScan: Bool = true) throws {
        let view = VStack(spacing: 14) {
            DiskUsageView(model: model, vm: vm, via: via, scopeChoices: scopes, allowScan: allowScan,
                          onAllowScans: via == .server ? {} : nil) {
                Picker("Disk view", selection: .constant(DiskSubTab.usage)) {
                    ForEach(DiskSubTab.allCases) { Text($0.displayName).tag($0) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize()
            }
        }
        .padding(.vertical, 16)
        .frame(width: 1320, height: 880)
        .background(Color(.sRGB, red: 0x1C / 255, green: 0x1C / 255, blue: 0x1E / 255))
        .environment(\.colorScheme, .dark)

        let host = NSHostingView(rootView: view)
        let rect = CGRect(x: 0, y: 0, width: 1320, height: 880)
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

    private func waitFor(_ cond: @escaping @MainActor () -> Bool) async throws {
        let end = Date().addingTimeInterval(3)
        while !cond() {
            if Date() > end { return XCTFail("condition not met") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    func testIdle() throws {
        let node = Node.localMac()
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "~")
        let vm = ServerViewModel(id: node.id, hostname: "mba", dnsName: "", osArch: "macOS")
        try render(m, vm: vm, via: .local, scopes: ["~", "/", "/System/Volumes/Data"], name: "usage-idle")
    }

    func testScanningOverSSH() async throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1")
        store.save(nodeID: node.id, scope: "/", result: try ScanFixtures.result("linux"),
                   finishedAt: Date().addingTimeInterval(-3 * 86_400))
        let fake = FakeDiskScanner()
        let started = Date().addingTimeInterval(-37)
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "/", now: { started })
        m.start()
        try await waitFor { fake.current != nil }
        fake.send(.progress(DiskScanProgress(
            files: 412_318, dirs: 30_211, bytes: 84_200_000_000, noAccessDirs: 7,
            current: "/var/lib/postgresql/16/main/base/16384", elapsedMs: 37_000, usedBytes: 182_000_000_000,
            top: [DiskScanTop(path: "/home/deploy", b: 52_100_000_000), DiskScanTop(path: "/var", b: 26_400_000_000),
                  DiskScanTop(path: "/usr", b: 5_700_000_000)]
        )))
        try await waitFor { m.progress != nil }
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .ssh(user: "deploy"), scopes: ["~", "/"], name: "usage-scanning-ssh")
        m.cancel()
    }

    func testResultMacWithSelection() throws {
        let node = Node.localMac()
        store.save(nodeID: node.id, scope: "~", result: try ScanFixtures.result("darwin"),
                   finishedAt: Date().addingTimeInterval(-120))
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "~")
        let vm = ServerViewModel(id: node.id, hostname: "mba", dnsName: "", osArch: "macOS")
        try render(m, vm: vm, via: .local, scopes: ["~", "/"], name: "usage-result-mac")
    }

    func testResultLinuxWithSelection() throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1")
        let r = try ScanFixtures.result("linux")
        store.save(nodeID: node.id, scope: "/", result: r, finishedAt: Date().addingTimeInterval(-300))
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "/")
        if let review = r.recs.first(where: { $0.group == .review && $0.elevated }) { m.toggle(review.id) }
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .ssh(user: "deploy"), scopes: ["~", "/"], name: "usage-result-linux")
    }

    func testResultWindows() throws {
        let node = Node(displayName: "win-dev", kind: .ssh, sshUser: "fritz", sshHost: "win-dev")
        let r = try ScanFixtures.result("windows")
        store.save(nodeID: node.id, scope: "~", result: r, finishedAt: Date().addingTimeInterval(-2 * 86_400))
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "~")
        let vm = ServerViewModel(id: node.id, hostname: "win-dev", dnsName: "", osArch: "Windows", kind: .ssh)
        try render(m, vm: vm, via: .ssh(user: "fritz"), scopes: ["~", "C:\\"], name: "usage-result-windows")
    }

    private func serverJob(_ node: Node, _ state: DiskScanJob.State, progress: DiskScanProgress? = nil) -> DiskScanJob {
        let created = Date().addingTimeInterval(-60)
        return DiskScanJob(
            id: "job-1", nodeId: node.id.uuidString, state: state, root: "/", progress: progress,
            createdTs: created, startedTs: created.addingTimeInterval(3),
            finishedTs: state.isFinal ? created.addingTimeInterval(51) : nil,
            resultBytes: state == .done ? 1_800_000 : 0
        )
    }

    func testRemoteStripDone() throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1", allowDiskScan: true)
        store.save(nodeID: node.id, scope: "/", result: try ScanFixtures.result("linux"), finishedAt: Date().addingTimeInterval(-5))
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "/")
        m.follow(job: serverJob(node, .done))
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .server, scopes: ["~", "/"], name: "usage-remote-strip")
    }

    func testRemoteStripRunning() async throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1", allowDiskScan: true)
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "/")
        m.start()
        try await waitFor { fake.current != nil }
        let p = DiskScanProgress(files: 212_004, dirs: 18_311, bytes: 41_000_000_000, current: "/var/lib/docker/overlay2",
                                 elapsedMs: 21_000, usedBytes: 182_000_000_000,
                                 top: [DiskScanTop(path: "/var", b: 30_000_000_000), DiskScanTop(path: "/home", b: 9_000_000_000)])
        fake.send(.job(serverJob(node, .running, progress: p)))
        try await waitFor { m.job != nil && m.progress != nil }
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .server, scopes: ["~", "/"], name: "usage-remote-running")
        m.cancel()
    }

    func testOptInOff() throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1")
        let m = DiskUsageModel(node: node, store: store, scanner: FakeDiskScanner(), scope: "/")
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .server, scopes: ["~", "/"], name: "usage-optin-off", allowScan: false)
    }

    func testOptInSamplerOff() async throws {
        let node = Node(displayName: "web-1", kind: .ssh, sshUser: "deploy", sshHost: "web-1", allowDiskScan: true)
        let fake = FakeDiskScanner()
        let m = DiskUsageModel(node: node, store: store, scanner: fake, scope: "/")
        m.start()
        try await waitFor { fake.current != nil }
        var j = serverJob(node, .failed)
        j.error = "disk scans are off on this host: run the sampler with --allow-disk-scan"
        fake.send(.job(j))
        fake.send(.error(j.error))
        try await waitFor { m.scansOff != nil }
        let vm = ServerViewModel(id: node.id, hostname: "web-1", dnsName: "", osArch: "Ubuntu", kind: .ssh)
        try render(m, vm: vm, via: .server, scopes: ["~", "/"], name: "usage-optin-sampler-off")
    }
}
