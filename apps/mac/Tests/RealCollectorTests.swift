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
}
