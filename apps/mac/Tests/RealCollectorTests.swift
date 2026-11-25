import XCTest
@testable import Towertail

private final class CountingInvoker: SamplerInvoker, @unchecked Sendable {
    private let lock = NSLock()
    private var ids: [UUID] = []

    var calledIDs: [UUID] { lock.withLock { ids } }

    func invokeOnce(node: Node) async throws -> Sample {
        lock.withLock { ids.append(node.id) }
        return makeSample(cpuPct: 10, memFrac: 0.2, diskFrac: 0.3)
    }
}

@MainActor
final class RealCollectorTests: XCTestCase {
    func testCollectorSkipsCloudNodes() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-collector-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("settings.json")

        let ssh = Node(displayName: "ssh-only", kind: .ssh, sshUser: "u", sshHost: "h")
        let linked = Node(displayName: "linked", kind: .ssh, sshUser: "u", sshHost: "h2",
                          cloud: CloudLink(enrolledAt: Date()))
        let cloudOnly = Node(displayName: "cloud-only", kind: .cloud)
        let store = NodeStore(nodes: [ssh, linked, cloudOnly], url: url)
        let invoker = CountingInvoker()
        let collector = RealCollector(
            nodeStore: store,
            settings: ServerSettings(url: url),
            invokerFactory: { _ in invoker }
        )
        let sink = ServerStore()

        let run = Task.detached { await collector.run(sink: sink) }
        let deadline = Date().addingTimeInterval(10)
        while invoker.calledIDs.isEmpty, Date() < deadline {
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        try await Task.sleep(nanoseconds: 300_000_000)
        run.cancel()

        XCTAssertEqual(invoker.calledIDs, [ssh.id])
        XCTAssertEqual(sink.serverVMs.count, 3, "each node still gets a view model")
    }

    // MARK: - Cloud rules

    private final class FakeCloudRules: CloudRulesSource {
        var byNode: [UUID: RemoteEffectiveRules] = [:]
        func effective(for nodeID: UUID) -> RemoteEffectiveRules? { byNode[nodeID] }
    }

    private func makeSettings(cpuWarn: Double) -> ServerSettings {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("tt-collector-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("settings.json")
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        let settings = ServerSettings(url: url)
        var t = settings.thresholds
        t.cpuWarn = cpuWarn
        t.cpuCritical = max(cpuWarn, t.cpuCritical)
        settings.thresholds = t
        return settings
    }

    private func cloudRules() -> RemoteEffectiveRules {
        var t = MetricThresholds.defaults
        t.cpuWarn = 0.8
        t.cpuCritical = 0.95
        var alerts = AlertRules.defaults
        alerts.cpu = AlertRule(sustainSeconds: 77, notify: .critical)
        return RemoteEffectiveRules(thresholds: t, alerts: alerts, muted: MutedSignals([.disk], long: true),
                                    ignore: ["backup"])
    }

    func testCloudHostUsesCloudRules() {
        let cloud = Node(displayName: "cloud", kind: .ssh, sshUser: "u", sshHost: "c", cloud: CloudLink(enrolledAt: Date()))
        let ssh = Node(displayName: "ssh", kind: .ssh, sshUser: "u", sshHost: "s")
        let settings = makeSettings(cpuWarn: 0.5)
        let source = FakeCloudRules()
        source.byNode[cloud.id] = cloudRules()
        source.byNode[ssh.id] = cloudRules()
        let store = ServerStore()
        RealCollector.syncViewModels(for: [cloud, ssh], store: store, settings: settings, cloud: source)
        let cloudVM = store.serverVMs.first { $0.id == cloud.id }
        let sshVM = store.serverVMs.first { $0.id == ssh.id }
        XCTAssertEqual(cloudVM?.thresholds.cpuWarn, 0.8)
        XCTAssertEqual(cloudVM?.alertRules.cpu.sustainSeconds, 77)
        XCTAssertEqual(cloudVM?.mutedSignals, MutedSignals([.disk], long: true))
        XCTAssertEqual(cloudVM?.longProcRule.notify, .off, "a muted long-running alert does not notify")
        XCTAssertEqual(cloudVM?.ignoredProcs, ["backup"])
        XCTAssertEqual(sshVM?.thresholds.cpuWarn, 0.5, "an SSH host keeps the local rules")
        XCTAssertNotEqual(sshVM?.alertRules.cpu.sustainSeconds, 77)
    }

    func testCloudRulesChangeReachesExistingViewModel() {
        let cloud = Node(displayName: "cloud", kind: .ssh, sshUser: "u", sshHost: "c", cloud: CloudLink(enrolledAt: Date()))
        let settings = makeSettings(cpuWarn: 0.5)
        let source = FakeCloudRules()
        let store = ServerStore()
        RealCollector.syncViewModels(for: [cloud], store: store, settings: settings, cloud: source)
        XCTAssertEqual(store.serverVMs.first?.thresholds.cpuWarn, 0.5, "no cloud rules yet")
        source.byNode[cloud.id] = cloudRules()
        RealCollector.syncViewModels(for: [cloud], store: store, settings: settings, cloud: source)
        XCTAssertEqual(store.serverVMs.first?.thresholds.cpuWarn, 0.8)
    }

    func testCloudHostWithoutRulesKeepsLocal() {
        let cloud = Node(displayName: "cloud", kind: .ssh, sshUser: "u", sshHost: "c", cloud: CloudLink(enrolledAt: Date()))
        let settings = makeSettings(cpuWarn: 0.5)
        let store = ServerStore()
        RealCollector.syncViewModels(for: [cloud], store: store, settings: settings, cloud: FakeCloudRules())
        XCTAssertEqual(store.serverVMs.first?.thresholds.cpuWarn, 0.5)
        RealCollector.syncViewModels(for: [cloud], store: store, settings: settings, cloud: nil)
        XCTAssertEqual(store.serverVMs.first?.thresholds.cpuWarn, 0.5)
    }
}
