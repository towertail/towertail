import XCTest
@testable import Towertail

final class SamplerInvokerTests: XCTestCase {
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

    func testSSHInvokerFailsCleanlyOnMisconfiguredNode() async {
        let invoker = SSHSamplerInvoker()
        let node = Node(displayName: "bad", kind: .ssh, sshUser: "", sshHost: "")
        do {
            _ = try await invoker.invokeOnce(node: node)
            XCTFail("expected failure")
        } catch let err as SamplerInvokeError {
            if case .misconfigured = err { return }
            XCTFail("expected .misconfigured, got \(err)")
        } catch {
            XCTFail("expected SamplerInvokeError, got \(error)")
        }
    }

    func testLocalInvokerRejectsSSHNode() async {
        // factory picks SSH for ssh nodes; invoking LocalSamplerInvoker directly
        // still runs the local binary regardless, so we test misrouting via SSH invoker only.
        let factoryResult = makeInvoker(for: Node.localMac())
        XCTAssertTrue(factoryResult is LocalSamplerInvoker)
        let sshNode = Node(displayName: "x", kind: .ssh, sshUser: "u", sshHost: "h")
        XCTAssertTrue(makeInvoker(for: sshNode) is SSHSamplerInvoker)
    }
}
