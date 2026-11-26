import XCTest
@testable import Towertail

final class ProcessKillerTests: XCTestCase {
    private func ssh(user: String? = nil, host: String? = nil) -> Node {
        Node(displayName: "n", kind: .ssh, sshUser: user, sshHost: host)
    }

    private func failure(_ outcome: ProcessKiller.Outcome) -> String? {
        if case .failure(let message) = outcome { return message }
        return nil
    }

    func testPreviewForEachNodeKind() {
        XCTAssertEqual(ProcessKiller.preview(pid: 42, node: nil), "kill -9 42")
        XCTAssertEqual(ProcessKiller.preview(pid: 42, node: .localMac()), "kill -9 42")
        XCTAssertEqual(ProcessKiller.preview(pid: 42, node: ssh(user: "ops", host: "db")), "ssh ops@db 'kill -9 42'")
        XCTAssertEqual(ProcessKiller.preview(pid: 42, node: ssh()), "ssh ?@? 'kill -9 42'")
        XCTAssertEqual(
            ProcessKiller.preview(pid: 42, node: Node(displayName: "c", kind: .cloud)),
            "Not available for a host that reports to Cloud."
        )
    }

    func testKillFailsWithoutCloudOrSSHTarget() async {
        let cloud = await ProcessKiller.kill(pid: 42, node: Node(displayName: "c", kind: .cloud))
        XCTAssertEqual(failure(cloud), "The app cannot stop processes on a host that reports to Cloud.")
        let noHost = await ProcessKiller.kill(pid: 42, node: ssh())
        XCTAssertEqual(failure(noHost), "SSH node missing user or host")
    }

    func testLocalKillOfMissingPidReportsExit() async {
        let outcome = await ProcessKiller.kill(pid: 99999, node: .localMac())
        XCTAssertTrue(failure(outcome)?.hasPrefix("kill exited 1") == true)
    }
}
