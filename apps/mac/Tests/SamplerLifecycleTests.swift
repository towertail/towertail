import XCTest
@testable import Towertail

private struct FakeEnroller: Enroller {
    var result: @Sendable () throws -> Enrollment

    func enroll(nodeID: UUID, displayName: String, machineID: String?) async throws -> Enrollment {
        try result()
    }
}

final class SamplerLifecycleTests: XCTestCase {
    private let node = Node(displayName: "web", kind: .ssh, sshUser: "u", sshHost: "h")
    private let enrollment = Enrollment(nodeId: UUID(), samplerToken: "tok-secret-123", endpoint: "https://api.example.com")
    private let binary = URL(fileURLWithPath: "/tmp/towertail-sampler")

    private func lifecycle(_ session: FakeSamplerSession, enroller: (any Enroller)? = nil) -> SamplerLifecycle {
        SamplerLifecycle(session: session, enroller: enroller, upload: { _ in "/home/u/.towertail/bin/towertail-sampler" })
    }

    private func sampleJSON(machineID: String?) -> String {
        let id = machineID.map { ",\"machine_id\":\"\($0)\"" } ?? ""
        return """
        {"v":1,"ts":"2026-04-20T12:00:00.000Z",
         "host":{"name":"h","os":"linux","arch":"arm64","kernel":"6.6","uptime_s":1,"sampler":"1.0.0+abc"\(id)},
         "cpu":{"pct":1.0,"load_1":0,"load_5":0,"load_15":0,"cores":4},
         "mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}
        """
    }

    func testStartPutsTokenFileNotArgv() async throws {
        let session = FakeSamplerSession()
        _ = try await lifecycle(session).start(enrollment: enrollment)

        XCTAssertEqual(session.puts.count, 1)
        let put = try XCTUnwrap(session.puts.first)
        XCTAssertEqual(put.mode, 0o600)
        XCTAssertTrue(put.path.hasSuffix(".towertail/sampler.token.in"))
        XCTAssertTrue(String(decoding: put.data, as: UTF8.self).contains("tok-secret-123"))
        let start = try XCTUnwrap(session.commands.first { $0.contains("sampler start") })
        XCTAssertTrue(start.contains("--token-file -"))
        XCTAssertFalse(session.commands.contains { $0.contains("tok-secret-123") })
    }

    func testMachineIDFromOnceSample() async {
        let session = FakeSamplerSession()
        session.reply("--once", sampleJSON(machineID: "abc"))
        let withID = await lifecycle(session).machineID()
        XCTAssertEqual(withID, "abc")

        session.reply("--once", sampleJSON(machineID: nil))
        let without = await lifecycle(session).machineID()
        XCTAssertNil(without)
    }

    func testStartAlreadyRunningIsSuccess() async throws {
        let session = FakeSamplerSession()
        session.reply("sampler start", "already running, pid 4121\n")
        let line = try await lifecycle(session).start(enrollment: enrollment)
        XCTAssertEqual(line, "already running, pid 4121")
    }

    func testStartFailureKeepsStderr() async {
        let session = FakeSamplerSession()
        session.fail("sampler start", RemoteCommandFailed(exitCode: 2, stderr: "join token rejected"))
        do {
            _ = try await lifecycle(session).start(enrollment: enrollment)
            XCTFail("expected startFailed")
        } catch {
            XCTAssertEqual(error as? LifecycleError, .startFailed("join token rejected"))
        }
    }

    func testEnrollPlanFullMapsTo402() async {
        let body = "{\"error\":{\"code\":\"plan_limit\",\"message\":\"the plan allows 3 hosts\"}}"
        let enroller = FakeEnroller { throw RemoteClient.RemoteError.http(402, body) }
        do {
            _ = try await lifecycle(FakeSamplerSession(), enroller: enroller).enroll(node: node)
            XCTFail("expected planFull")
        } catch {
            XCTAssertEqual(error as? LifecycleError, .planFull(max: 3))
        }
    }

    func testEnrollNeedsSignIn() async {
        do {
            _ = try await lifecycle(FakeSamplerSession()).enroll(node: node)
            XCTFail("expected notSignedIn")
        } catch {
            XCTAssertEqual(error as? LifecycleError, .notSignedIn)
        }
    }

    func testEnableRebootReturnsMode() async throws {
        let session = FakeSamplerSession()
        session.reply("boot enable", "{\"reboot\":\"cron\"}\n")
        let mode = try await lifecycle(session).enableReboot()
        XCTAssertEqual(mode, "cron")
    }

    func testEnableRebootFailureIsNoBootMethod() async {
        let session = FakeSamplerSession()
        session.fail("boot enable", RemoteCommandFailed(exitCode: 1, stderr: "no way"))
        do {
            _ = try await lifecycle(session).enableReboot()
            XCTFail("expected noBootMethod")
        } catch {
            XCTAssertEqual(error as? LifecycleError, .noBootMethod)
        }
    }

    func testStatusDecodes() async throws {
        let session = FakeSamplerSession()
        session.reply(" status", """
        {"running":true,"pid":4121,"version":"1.4.0+abc","endpoint":"https://api.example.com",\
        "last_push":"2026-11-20T10:00:00.123456Z","last_status":202,"last_error":"","update":"",\
        "interval":"5s","paused_until":null,"gone":"","reboot":"linger"}
        """)
        let status = try await lifecycle(session).status()
        XCTAssertTrue(status.running)
        XCTAssertEqual(status.pid, 4121)
        XCTAssertEqual(status.reboot, "linger")
        XCTAssertNotNil(status.lastPush)
        XCTAssertNil(status.pausedUntil)
    }

    func testStatusBadJSON() async {
        let session = FakeSamplerSession()
        session.reply(" status", "not json")
        do {
            _ = try await lifecycle(session).status()
            XCTFail("expected statusUnreadable")
        } catch {
            XCTAssertEqual(error as? LifecycleError, .statusUnreadable("not json"))
        }
    }

    func testRemoveRunsStopThenBootDisable() async throws {
        let session = FakeSamplerSession()
        try await lifecycle(session).remove()
        let c = session.commands
        XCTAssertEqual(c.count, 3)
        XCTAssertTrue(c[0].hasSuffix("towertail-sampler stop"))
        XCTAssertTrue(c[1].hasSuffix("towertail-sampler boot disable"))
        XCTAssertTrue(c[2].hasPrefix("rm -f"))
    }

    func testTripleOfWindowsHostIsUnsupported() async {
        let session = FakeSamplerSession()
        session.fail("uname -sm", RemoteCommandFailed(exitCode: 1, stderr: "not recognized"))
        do {
            _ = try await lifecycle(session).triple()
            XCTFail("expected unsupportedHost")
        } catch {
            guard case LifecycleError.unsupportedHost = error else { return XCTFail("got \(error)") }
            XCTAssertEqual(error.localizedDescription, "Not supported on this host")
        }
    }

    func testUpdateStopsUploadsStarts() async throws {
        let session = FakeSamplerSession()
        let uploaded = LockedBox<Bool>(false)
        let life = SamplerLifecycle(session: session, enroller: nil, upload: { _ in
            uploaded.set(true)
            return "x"
        })
        try await life.update(binary: binary)
        XCTAssertTrue(uploaded.get())
        XCTAssertTrue(session.commands.first?.hasSuffix("stop") == true)
        XCTAssertTrue(session.commands.last?.hasSuffix("towertail-sampler start") == true)
    }
}

/// A value that a `@Sendable` closure can change in a test.
final class LockedBox<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: T

    init(_ value: T) { self.value = value }
    func get() -> T { lock.withLock { value } }
    func set(_ new: T) { lock.withLock { value = new } }
}
