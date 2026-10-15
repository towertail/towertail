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
    }

    func testNodeRoundTripPreservesSshFields() throws {
        let original = Node(
            displayName: "new",
            kind: .ssh,
            sshUser: "u",
            sshHost: "h",
            sshPort: 2222,
            authMethod: .password,
            knownHostFingerprint: "SHA256:ZZZZ"
        )
        let enc = JSONEncoder()
        let data = try enc.encode(original)
        let decoded = try JSONDecoder().decode(Node.self, from: data)
        XCTAssertEqual(decoded.sshPort, 2222)
        XCTAssertEqual(decoded.authMethod, .password)
        XCTAssertEqual(decoded.knownHostFingerprint, "SHA256:ZZZZ")
    }
}
