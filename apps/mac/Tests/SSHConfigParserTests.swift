import XCTest
@testable import Towertail

final class SSHConfigParserTests: XCTestCase {
    func testParsesHostUserPort() {
        let hosts = SSHConfigParser.parse("""
        # my servers
        Host web
            HostName 10.0.0.5
            User ana
            Port 2222
        Host db
          HostName=db.example.com
          User=bob
        """)
        XCTAssertEqual(hosts, [
            SSHConfigHost(alias: "web", hostName: "10.0.0.5", user: "ana", port: 2222),
            SSHConfigHost(alias: "db", hostName: "db.example.com", user: "bob", port: nil),
        ])
    }

    func testSkipsWildcards() {
        let hosts = SSHConfigParser.parse("""
        Host *
            User root
        Host web-? staging-*
            User ana
        Host real
            HostName 1.2.3.4
        """)
        XCTAssertEqual(hosts.map(\.alias), ["real"])
        XCTAssertNil(hosts.first?.user)
    }

    func testHostNameDefaultsToAlias() {
        let hosts = SSHConfigParser.parse("Host box.example.com\n  User ana\n")
        XCTAssertEqual(hosts.first?.sshHost, "box.example.com")
        XCTAssertNil(hosts.first?.hostName)
    }

    func testIgnoresMatchBlocks() {
        let hosts = SSHConfigParser.parse("""
        Host web
            HostName 1.1.1.1
        Match host *.corp
            HostName 9.9.9.9
            User evil
        Host db
            HostName 2.2.2.2
        """)
        XCTAssertEqual(hosts.map(\.alias), ["web", "db"])
        XCTAssertEqual(hosts[0].hostName, "1.1.1.1")
        XCTAssertNil(hosts[0].user)
    }

    func testIncludeReadsOneLevel() {
        let hosts = SSHConfigParser.parse("Include extra\nHost a\n  HostName 1.1.1.1\n", include: { pattern in
            pattern == "extra" ? "Host b\n  HostName 2.2.2.2\nInclude deeper\n" : "Host c\n"
        })
        XCTAssertEqual(hosts.map(\.alias), ["b", "a"])
    }
}
