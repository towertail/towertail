import XCTest
@testable import Towertail

@MainActor
final class SamplerKeeperTests: XCTestCase {
    private var nodes: NodeStore!
    private var node: Node!
    private var session: FakeSamplerSession!
    private var visits: LockedBox<Int>!
    private var uploads: LockedBox<Int>!
    private var clock: LockedBox<Date>!

    override func setUp() async throws {
        try await super.setUp()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-keeper-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h", cloud: CloudLink(enrolledAt: Date()))
        nodes = NodeStore(nodes: [node], url: dir.appendingPathComponent("settings.json"))
        session = FakeSamplerSession()
        visits = LockedBox(0)
        uploads = LockedBox(0)
        clock = LockedBox(Date(timeIntervalSince1970: 1_000_000))
    }

    private func keeper(manifest: SamplerManifest? = nil) -> SamplerKeeper {
        let session = self.session!
        let visits = self.visits!
        let uploads = self.uploads!
        let clock = self.clock!
        return SamplerKeeper(
            alerted: { [] },
            nodes: nodes,
            manifest: manifest,
            connect: { _ in
                visits.set(visits.get() + 1)
                return session
            },
            upload: { _, _ in
                uploads.set(uploads.get() + 1)
                return "x"
            },
            binary: { _ in URL(fileURLWithPath: "/tmp/towertail-sampler") },
            now: { clock.get() }
        )
    }

    private func status(running: Bool, version: String = "1.4.0+abc", update: String = "") -> String {
        "{\"running\":\(running),\"pid\":\(running ? 7 : 0),\"version\":\"\(version)\",\"last_error\":\"\",\"update\":\"\(update)\",\"reboot\":\"cron\"}"
    }

    func testAlertedAndStoppedRestarts() async {
        session.reply(" status", status(running: false))
        await keeper().handle([node.id])
        XCTAssertTrue(session.commands.contains { $0.hasSuffix("towertail-sampler start") })
        XCTAssertEqual(nodes.node(withId: node.id)?.cloud?.lastStatus?.running, false)
    }

    func testAlertedAndRunningDoesNotRestart() async {
        session.reply(" status", status(running: true))
        await keeper().handle([node.id])
        XCTAssertFalse(session.commands.contains { $0.contains("sampler start") })
        XCTAssertEqual(nodes.node(withId: node.id)?.cloud?.lastStatus?.pid, 7)
    }

    func testOneTryEach10Minutes() async {
        session.reply(" status", status(running: true))
        let keeper = keeper()
        await keeper.handle([node.id])
        clock.set(clock.get().addingTimeInterval(300))
        await keeper.handle([node.id])
        XCTAssertEqual(visits.get(), 1)
        clock.set(clock.get().addingTimeInterval(400))
        await keeper.handle([node.id])
        XCTAssertEqual(visits.get(), 2)
    }

    func testHostWithoutCloudLinkIsSkipped() async {
        var plain = node!
        plain.cloud = nil
        nodes.update(plain)
        await keeper().handle([node.id])
        XCTAssertEqual(visits.get(), 0)
    }

    func testUpdateWhenStatusHasUpdate() async {
        session.reply(" status", status(running: true, version: "1.3.0+old", update: "1.4.0"))
        let manifest = SamplerManifest(version: "1.4.0", sha: "abc", binaries: [:])
        await keeper(manifest: manifest).handle([node.id])
        XCTAssertEqual(uploads.get(), 1)
        XCTAssertTrue(session.commands.contains { $0.hasSuffix("towertail-sampler stop") })
        XCTAssertTrue(session.commands.last?.hasSuffix("towertail-sampler start") == true)
    }

    func testNoUpdateWhenBundledVersionDiffers() async {
        session.reply(" status", status(running: true, version: "1.3.0+old", update: "1.5.0"))
        let manifest = SamplerManifest(version: "1.4.0", sha: "abc", binaries: [:])
        await keeper(manifest: manifest).handle([node.id])
        XCTAssertEqual(uploads.get(), 0)
    }
}
