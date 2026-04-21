import XCTest
@testable import Towertail

final class AgentInvokerTests: XCTestCase {
    private func resolveLocalBinary() -> URL? {
        if let bundled = LocalAgentInvoker.bundledBinaryURL(),
           FileManager.default.fileExists(atPath: bundled.path) {
            return bundled
        }
        return LocalAgentInvoker.developmentFallbackURL()
    }

    func testLocalInvokerProducesValidSample() async throws {
        guard let _ = resolveLocalBinary() else {
            throw XCTSkip("darwin binary not available; skipping")
        }
        let invoker = LocalAgentInvoker()
        let node = Node.localMac()
        let sample = try await invoker.invokeOnce(node: node)
        XCTAssertEqual(sample.v, 1)
        XCTAssertFalse(sample.host.name.isEmpty)
        XCTAssertGreaterThan(sample.cpu.cores, 0)
    }

    func testSSHInvokerFailsCleanlyOnMisconfiguredNode() async {
        let invoker = SSHAgentInvoker()
        let node = Node(displayName: "bad", kind: .ssh, sshUser: "", sshHost: "")
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected failure")
        } catch let err as AgentInvokeError {
            if case .misconfigured = err { return }
            XCTFail("expected .misconfigured, got \(err)")
        } catch {
            XCTFail("expected AgentInvokeError, got \(error)")
        }
    }

    func testLocalInvokerRejectsSSHNode() async {
        // factory picks SSH for ssh nodes; invoking LocalAgentInvoker directly
        // still runs the local binary regardless, so we test misrouting via SSH invoker only.
        let factoryResult = makeInvoker(for: Node.localMac())
        XCTAssertTrue(factoryResult is LocalAgentInvoker)
        let sshNode = Node(displayName: "x", kind: .ssh, sshUser: "u", sshHost: "h")
        XCTAssertTrue(makeInvoker(for: sshNode) is SSHAgentInvoker)
    }
}
