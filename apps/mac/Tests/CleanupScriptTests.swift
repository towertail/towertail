import XCTest
@testable import Towertail

final class CleanupScriptTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_792_000_000)

    private func sample(os: String = "linux", host: String = "web-1") -> DiskScanResult {
        DiskScanResult(
            root: "/", os: os, hostname: host, startedTs: "2026-10-15T09:12:00Z",
            scannedBytes: 3_000_000_000,
            tree: DiskNode(n: "/", b: 3_000_000_000),
            recs: [
                DiskRec(id: "a", group: .safe, label: "npm cache", path: "~/.npm/_cacache", bytes: 1_000_000_000,
                        cmd: DiskRecCmd(sh: ["rm -rf ~/.npm/_cacache"], pwsh: ["Remove-Item -Recurse -Force \"$env:LOCALAPPDATA\\npm-cache\""])),
                DiskRec(id: "b", group: .review, label: "journal", path: "/var/log/journal", bytes: 2_000_000_000,
                        elevated: true,
                        cmd: DiskRecCmd(sh: ["sudo journalctl --vacuum-size=200M"], pwsh: ["Clear-RecycleBin -Force"])),
            ]
        )
    }

    func testBashScriptHasGuardAndSelectedCommandsInOrder() throws {
        let s = CleanupScript(result: sample(), selected: ["b", "a"], now: now)
        let text = s.text
        XCTAssertEqual(s.totalBytes, 3_000_000_000)
        XCTAssertEqual(s.shellLabel, "bash · Linux")
        XCTAssertTrue(text.hasPrefix("# Towertail cleanup for web-1"))
        XCTAssertTrue(text.contains("set -u\n"))
        XCTAssertTrue(text.contains(#"[ "$(hostname)" = 'web-1' ] || { echo 'wrong host: this script is for web-1'; exit 1; }"#))
        XCTAssertTrue(text.contains("# sudo lines ask for your password."))
        let a = try XCTUnwrap(text.range(of: "rm -rf ~/.npm/_cacache"))
        let b = try XCTUnwrap(text.range(of: "sudo journalctl"))
        let guardLine = try XCTUnwrap(text.range(of: "hostname"))
        XCTAssertLessThan(guardLine.lowerBound, a.lowerBound)
        XCTAssertLessThan(a.lowerBound, b.lowerBound)
        XCTAssertFalse(text.contains("Remove-Item"))
        try assertBashParses(text)
    }

    func testBashSyntaxOfFixtureScripts() throws {
        for os in ["linux", "darwin"] {
            let r = try ScanFixtures.result(os)
            let s = CleanupScript(result: r, selected: Set(r.recs.map(\.id)), now: now)
            XCTAssertEqual(s.chosen.count, r.recs.count)
            try assertBashParses(s.text)
        }
    }

    func testPowerShellScript() {
        let s = CleanupScript(result: sample(os: "windows", host: "DESKTOP-7"), selected: ["a", "b"], now: now)
        let text = s.text
        XCTAssertEqual(s.shellLabel, "PowerShell · Windows")
        XCTAssertTrue(text.contains("if ([System.Net.Dns]::GetHostName() -ne 'DESKTOP-7') { Write-Host 'wrong host: this script is for DESKTOP-7'; return }"))
        XCTAssertTrue(text.contains("# Lines marked admin need an elevated PowerShell."))
        XCTAssertTrue(text.contains("  Remove-Item -Recurse -Force"))
        XCTAssertFalse(text.contains("set -u"))
        XCTAssertFalse(text.contains("rm -rf"))
        XCTAssertTrue(text.hasSuffix("}\n"))
    }

    func testNoElevationNoteWithoutElevatedItems() {
        let text = CleanupScript(result: sample(), selected: ["a"], now: now).text
        XCTAssertFalse(text.contains("sudo lines"))
        XCTAssertTrue(text.contains("# 1 item · 1.0 GB."))
    }

    func testEmptySelection() throws {
        let s = CleanupScript(result: sample(), selected: [], now: now)
        XCTAssertEqual(s.totalBytes, 0)
        XCTAssertTrue(s.text.contains("# Select items above to build the script."))
        XCTAssertFalse(s.text.contains("hostname"))
        try assertBashParses(s.text)
    }

    func testQuotesHostWithQuote() throws {
        let text = CleanupScript(result: sample(host: "o'brien"), selected: ["a"], now: now).text
        XCTAssertTrue(text.contains(#"= 'o'\''brien' ]"#))
        try assertBashParses(text)
    }

    private func assertBashParses(_ script: String, file: StaticString = #filePath, line: UInt = #line) throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("tt-cleanup-\(UUID().uuidString).sh")
        try script.write(to: url, atomically: true, encoding: .utf8)
        defer { try? FileManager.default.removeItem(at: url) }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/bash")
        p.arguments = ["-n", url.path]
        let err = Pipe()
        p.standardError = err
        try p.run()
        p.waitUntilExit()
        let msg = String(decoding: err.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        XCTAssertEqual(p.terminationStatus, 0, "bash -n: \(msg)", file: file, line: line)
    }
}
