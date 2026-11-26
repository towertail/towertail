import XCTest
@testable import Towertail

/// Tests for the sampler invoker layer after the Citadel migration. Focus is
/// on pure logic — auth-method selection, node routing, fingerprint format,
/// and old-JSON decoding — since real SSH sessions need a running server.
final class SamplerInvokerTests: XCTestCase {

    // MARK: - Local invoker (unchanged path)

    private func resolveLocalBinary() -> URL? {
        if let bundled = LocalSamplerInvoker.bundledBinaryURL(),
           FileManager.default.fileExists(atPath: bundled.path) {
            return bundled
        }
        return LocalSamplerInvoker.developmentFallbackURL()
    }

    func testLocalInvokerProducesValidSample() async throws {
        guard let _ = resolveLocalBinary() else {
            throw XCTSkip("darwin binary not available; skipping")
        }
        let invoker = LocalSamplerInvoker()
        let node = Node.localMac()
        let sample = try await invoker.invokeOnce(node: node)
        XCTAssertEqual(sample.v, 1)
        XCTAssertFalse(sample.host.name.isEmpty)
        XCTAssertGreaterThan(sample.cpu.cores, 0)
    }

    /// The bundled sampler accepts the flag and sends no folders with it.
    func testLocalInvokerKeepsFoldersOffWhenAsked() async throws {
        guard resolveLocalBinary() != nil else { throw XCTSkip("darwin binary not available; skipping") }
        var node = Node.localMac()
        node.sendProcFolders = false
        let sample = try await LocalSamplerInvoker().invokeOnce(node: node)
        let items = sample.procs?.items ?? []
        XCTAssertTrue(items.allSatisfy { $0.cwd == nil && $0.repo == nil })
    }

    // MARK: - Factory routing

    func testMakeInvokerReturnsLocalForLocalNode() {
        let invoker = makeInvoker(for: Node.localMac())
        XCTAssertTrue(invoker is LocalSamplerInvoker)
    }

    func testMakeInvokerReturnsSSHForSSHNode() {
        let node = Node(displayName: "x", kind: .ssh, sshUser: "u", sshHost: "h")
        let invoker = makeInvoker(for: node)
        XCTAssertTrue(invoker is SSHSamplerInvoker)
    }

    // MARK: - buildAuthMethods

    func testBuildAuthMethodsRequiresUser() {
        let node = Node(displayName: "x", kind: .ssh, sshUser: "", sshHost: "h")
        XCTAssertThrowsError(
            try SSHConnectionFactory.buildAuthMethods(node: node, keychainPassword: nil)
        ) { err in
            XCTAssertTrue(err is SSHConnectionFactory.NoCredential, "expected NoCredential, got \(err)")
        }
    }

    func testBuildAuthMethodsPasswordModeNeedsKeychainEntry() {
        let node = Node(
            displayName: "x", kind: .ssh,
            sshUser: "u", sshHost: "h",
            authMethod: .password
        )
        XCTAssertThrowsError(
            try SSHConnectionFactory.buildAuthMethods(node: node, keychainPassword: nil)
        ) { err in
            XCTAssertTrue(err is SSHConnectionFactory.NoCredential, "expected NoCredential, got \(err)")
        }
    }

    func testBuildAuthMethodsPasswordModeReturnsOneMethod() throws {
        let node = Node(
            displayName: "x", kind: .ssh,
            sshUser: "u", sshHost: "h",
            authMethod: .password
        )
        let methods = try SSHConnectionFactory.buildAuthMethods(
            node: node, keychainPassword: "hunter2"
        )
        XCTAssertEqual(methods.count, 1)
    }

    // MARK: - fingerprint / mismatch semantics

    func testHostKeyMismatchErrorMessageIncludesBothKeys() {
        let err = SSHConnectionFactory.HostKeyMismatch(
            expected: "SHA256:AAAA",
            actual: "SHA256:BBBB",
            host: "example.com"
        )
        XCTAssertTrue(err.errorDescription?.contains("AAAA") ?? false)
        XCTAssertTrue(err.errorDescription?.contains("BBBB") ?? false)
        XCTAssertTrue(err.errorDescription?.contains("example.com") ?? false)
    }

    // MARK: - Node Codable round-trip (old & new)

    /// Old settings.json without the SSH migration fields must decode into
    /// reasonable defaults (port=nil → 22, authMethod=.key, no fingerprint).
    func testNodeDecodesOldSchemaWithDefaults() throws {
        let oldJson = """
        {
            "id": "11111111-1111-1111-1111-111111111111",
            "displayName": "old",
            "kind": "ssh",
            "sshUser": "root",
            "sshHost": "10.0.0.1",
            "tags": [],
            "enabled": true,
            "iconOnWarn": true,
            "iconOnCritical": true,
            "notifyOnWarn": true,
            "notifyOnCritical": true,
            "favorite": false
        }
        """.data(using: .utf8)!

        let node = try JSONDecoder().decode(Node.self, from: oldJson)
        XCTAssertEqual(node.displayName, "old")
        XCTAssertEqual(node.kind, .ssh)
        XCTAssertNil(node.sshPort)
        XCTAssertEqual(node.effectiveSshPort, 22)
        XCTAssertEqual(node.authMethod, .key)
        XCTAssertNil(node.knownHostFingerprint)
        XCTAssertTrue(node.sendProcFolders)
    }

    func testNodeRoundTripPreservesSshFields() throws {
        let original = Node(
            displayName: "new",
            kind: .ssh,
            sshUser: "u",
            sshHost: "h",
            sshPort: 2222,
            authMethod: .password,
            knownHostFingerprint: "SHA256:ZZZZ",
            sendProcFolders: false
        )
        let enc = JSONEncoder()
        let data = try enc.encode(original)
        let decoded = try JSONDecoder().decode(Node.self, from: data)
        XCTAssertEqual(decoded.sshPort, 2222)
        XCTAssertEqual(decoded.authMethod, .password)
        XCTAssertEqual(decoded.knownHostFingerprint, "SHA256:ZZZZ")
        XCTAssertFalse(decoded.sendProcFolders)
    }

    func testSSHURLUsesPort() {
        let withPort = Node(displayName: "n", kind: .ssh, sshUser: "alice", sshHost: "h", sshPort: 2222)
        XCTAssertEqual(withPort.sshURL?.absoluteString, "ssh://alice@h:2222")

        let noPort = Node(displayName: "n", kind: .ssh, sshUser: "alice", sshHost: "h", sshPort: nil)
        XCTAssertEqual(noPort.sshURL?.absoluteString, "ssh://alice@h")

        let noUser = Node(displayName: "n", kind: .ssh, sshUser: "", sshHost: "h", sshPort: 2222)
        XCTAssertEqual(noUser.sshURL?.absoluteString, "ssh://h:2222")

        XCTAssertNil(Node(displayName: "n", kind: .local).sshURL)
        XCTAssertNil(Node(displayName: "n", kind: .ssh, sshHost: "").sshURL)
    }
}

// MARK: - Timeout

/// A session whose command hangs until `close()`.
private final class HangingSession: SamplerSession, @unchecked Sendable {
    private let lock = NSLock()
    private var waiter: CheckedContinuation<Data, Error>?
    private var closedFlag = false

    var closed: Bool { lock.lock(); defer { lock.unlock() }; return closedFlag }

    func run(_ command: String) async throws -> Data {
        try await withCheckedThrowingContinuation { c in
            lock.lock()
            if closedFlag { lock.unlock(); c.resume(throwing: CancellationError()); return }
            waiter = c
            lock.unlock()
        }
    }

    func put(_ data: Data, path: String, mode: Int) async throws {}

    func close() async {
        lock.lock()
        closedFlag = true
        let w = waiter
        waiter = nil
        lock.unlock()
        w?.resume(throwing: CancellationError())
    }
}

final class SSHSamplerInvokerTimeoutTests: XCTestCase {
    private let node = Node(displayName: "x", kind: .ssh, sshUser: "u", sshHost: "h")

    private func assertTimeout(_ invoker: SSHSamplerInvoker, file: StaticString = #filePath, line: UInt = #line) async {
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected timeout", file: file, line: line)
        } catch SamplerInvokeError.timeout {
        } catch {
            XCTFail("expected timeout, got \(error)", file: file, line: line)
        }
    }

    func testHungCommandTimesOutAndClosesClient() async throws {
        let session = HangingSession()
        let invoker = SSHSamplerInvoker(timeout: 0.2) { _ in session }
        let start = Date()
        await assertTimeout(invoker)
        XCTAssertLessThan(Date().timeIntervalSince(start), 2)
        try await waitFor(timeout: 2) { session.closed }
    }

    func testHungConnectTimesOutAndClosesLateClient() async throws {
        let session = HangingSession()
        let invoker = SSHSamplerInvoker(timeout: 0.2) { _ in
            try await Task.sleep(nanoseconds: 600_000_000)
            return session
        }
        await assertTimeout(invoker)
        XCTAssertFalse(session.closed)
        try await waitFor(timeout: 2) { session.closed }
    }

    func testFastCommandIsNotAffected() async throws {
        struct EmptySession: SamplerSession {
            func run(_ command: String) async throws -> Data { Data() }
            func put(_ data: Data, path: String, mode: Int) async throws {}
            func close() async {}
        }
        let invoker = SSHSamplerInvoker(timeout: 5) { _ in EmptySession() }
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected emptyOutput")
        } catch SamplerInvokeError.emptyOutput {
        }
    }

    func testExit127MapsToRemoteBinaryMissing() async {
        let session = FakeSamplerSession()
        session.fail("--once", RemoteCommandFailed(exitCode: 127, stderr: "not found"))
        let invoker = SSHSamplerInvoker(timeout: 5) { _ in session }
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected remoteBinaryMissing")
        } catch SamplerInvokeError.remoteBinaryMissing {
        } catch {
            XCTFail("got \(error)")
        }
    }

    func testOtherExitCodeKeepsCodeAndStderr() async {
        let session = FakeSamplerSession()
        session.fail("--once", RemoteCommandFailed(exitCode: 2, stderr: "boom"))
        let invoker = SSHSamplerInvoker(timeout: 5) { _ in session }
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected sshFailed")
        } catch SamplerInvokeError.sshFailed(let stderr, let code) {
            XCTAssertEqual(stderr, "boom")
            XCTAssertEqual(code, 2)
        } catch {
            XCTFail("got \(error)")
        }
    }

    // MARK: - Process folders

    private static let sampleJSON = """
    {"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}
    """

    private func session() -> FakeSamplerSession {
        let s = FakeSamplerSession()
        s.reply("--once", Self.sampleJSON)
        return s
    }

    func testSSHCommandHasNoProcCwdWhenOff() async throws {
        let s = session()
        var off = node
        off.sendProcFolders = false
        _ = try await SSHSamplerInvoker(timeout: 5) { _ in s }.invokeOnce(node: off)
        XCTAssertEqual(s.commands, ["~/.towertail/bin/towertail-sampler --once --no-proc-cwd"])
    }

    func testSSHCommandPlainWhenOn() async throws {
        let s = session()
        _ = try await SSHSamplerInvoker(timeout: 5) { _ in s }.invokeOnce(node: node)
        XCTAssertEqual(s.commands, ["~/.towertail/bin/towertail-sampler --once"])
    }

    func testSSHRetriesWithoutFlagForOldSampler() async throws {
        let s = session()
        s.fail("--no-proc-cwd", RemoteCommandFailed(exitCode: 1, stderr: "flag provided but not defined: -no-proc-cwd\n"))
        var off = node
        off.sendProcFolders = false
        let sample = try await SSHSamplerInvoker(timeout: 5) { _ in s }.invokeOnce(node: off)
        XCTAssertEqual(sample.host.name, "h")
        XCTAssertEqual(s.commands, [
            "~/.towertail/bin/towertail-sampler --once --no-proc-cwd",
            "~/.towertail/bin/towertail-sampler --once",
        ])
    }

    func testSSHDoesNotRetryOtherFailures() async {
        let s = session()
        s.fail("--no-proc-cwd", RemoteCommandFailed(exitCode: 1, stderr: "boom"))
        var off = node
        off.sendProcFolders = false
        do {
            _ = try await SSHSamplerInvoker(timeout: 5) { _ in s }.invokeOnce(node: off)
            XCTFail("expected sshFailed")
        } catch SamplerInvokeError.sshFailed(let stderr, _) {
            XCTAssertEqual(stderr, "boom")
        } catch {
            XCTFail("got \(error)")
        }
        XCTAssertEqual(s.commands.count, 1)
    }

    func testFlagDetectsUnknownFlagText() {
        XCTAssertTrue(ProcFolderFlag.isUnknownFlag("flag provided but not defined: -no-proc-cwd"))
        XCTAssertFalse(ProcFolderFlag.isUnknownFlag("flag provided but not defined: -other"))
        XCTAssertFalse(ProcFolderFlag.isUnknownFlag("permission denied"))
    }
}
