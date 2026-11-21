import XCTest
@testable import Towertail

final class ProcCulpritTests: XCTestCase {
    func testWhatTable() {
        let rows: [(String, String?, String?)] = [
            ("node", "node /p/node_modules/.bin/vitest run --coverage", "vitest run"),
            ("node", "node --max-old-space-size=4096 /p/node_modules/.bin/next dev", "next dev"),
            ("node", "node /app/server.js --port 3000", "server.js"),
            ("node", "/usr/local/bin/node /usr/local/bin/npm run dev", "npm run dev"),
            ("python3.12", "/usr/bin/python3.12 -m pytest tests/", "pytest"),
            ("python3", "python3 manage.py runserver 0.0.0.0:8000", "manage.py runserver"),
            ("go", "go test ./...", "go test"),
            ("java", "java -Xmx2g -jar /opt/kafka.jar", "kafka.jar"),
            ("node.exe", "\"C:\\Program Files\\nodejs\\node.exe\" C:\\x\\vitest.mjs run", "vitest.mjs run"),
            ("postgres", "postgres: main", nil),
            ("node", "node -e \"...\"", nil),
            ("node", nil, nil),
            ("node", "  ", nil),
            ("node", "node -r ts-node/register /p/main.js", "main.js"),
            ("java", "java -cp /a.jar com.acme.Main", "Main"),
        ]
        for (name, cmd, want) in rows {
            XCTAssertEqual(ProcCulprit.what(name: name, cmd: cmd), want, "\(name): \(cmd ?? "nil")")
        }
    }

    func testWhatCutAt28() {
        let script = String(repeating: "a", count: 40) + ".js"
        let what = ProcCulprit.what(name: "node", cmd: "node /p/\(script)")
        XCTAssertEqual(what, String(repeating: "a", count: 27) + "…")
    }

    private func node(_ pid: Int32, cpu: Double, rss: Int64 = 0) -> ProcSample {
        ProcSample(pid: pid, name: "node", cmd: "node /p/node_modules/.bin/vitest run", cpuPct: cpu, rss: rss)
    }

    func testTopMergesSameName() {
        let items = [node(1, cpu: 100), node(2, cpu: 90), node(3, cpu: 70), node(4, cpu: 50),
                     ProcSample(pid: 9, name: "postgres", cpuPct: 40, rss: 0)]
        let top = ProcCulprit.top(items, by: .cpu, limit: 3)
        XCTAssertEqual(top.map(\.label), ["node ×4 · vitest run", "postgres"])
        XCTAssertEqual(ProcCulprit.Ranking.cpu.text(top[0]), "310%")
        XCTAssertEqual(top[0].pids.count, 4)
    }

    func testAppBundleMerges() {
        let cmd = "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper (Renderer).app/Contents/MacOS/Google Chrome Helper (Renderer) --type=renderer"
        let items = (1...3).map {
            ProcSample(pid: Int32($0), name: "Google Chrome Helper (Renderer)", cmd: cmd, cpuPct: 10, rss: 0)
        }
        let top = ProcCulprit.top(items, by: .cpu, limit: 3)
        XCTAssertEqual(top.map(\.label), ["Google Chrome.app ×3"])
    }

    func testAppBundleIgnoresArguments() {
        let p = ProcSample(pid: 1, name: "node", cmd: "/usr/local/bin/node /x/Bar.app/s.js", cpuPct: 1, rss: 0)
        XCTAssertEqual(ProcCulprit(p).key.name, "node")
    }

    func testTopMemSumsRss() {
        let gb: Int64 = 1_073_741_824
        let items = [node(1, cpu: 1, rss: gb), node(2, cpu: 1, rss: gb)]
        let top = ProcCulprit.top(items, by: .mem, limit: 3)
        XCTAssertEqual(ProcCulprit.Ranking.mem.text(top[0]), ProcessTable.formatBytes(2 * gb))
    }

    func testTopLimitAndTieOrder() {
        let items = ["d", "b", "c", "a"].enumerated().map {
            ProcSample(pid: Int32($0.offset), name: $0.element, cpuPct: 5, rss: 0)
        }
        let top = ProcCulprit.top(items, by: .cpu, limit: 3)
        XCTAssertEqual(top.map(\.name), ["a", "b", "c"])
    }

    func testWhatComesFromTheTopMember() {
        let items = [
            ProcSample(pid: 1, name: "node", cmd: "node /p/low.js", cpuPct: 5, rss: 0),
            ProcSample(pid: 2, name: "node", cmd: "node /p/high.js", cpuPct: 50, rss: 0),
        ]
        XCTAssertEqual(ProcCulprit.top(items, by: .cpu, limit: 1).first?.what, "high.js")
    }

    func testLabelForms() {
        XCTAssertEqual(ProcCulprit(name: "node", pid: 1, cmd: nil).label, "node")
        XCTAssertEqual(ProcCulprit(name: "", pid: 812, cmd: nil).label, "pid 812")
        XCTAssertEqual(ProcCulprit(name: "node", pid: 1, cmd: "node vitest run").label, "node · vitest run")
        let go = ProcCulprit.top(
            (1...3).map { ProcSample(pid: Int32($0), name: "go", cmd: "go test ./...", cpuPct: 1, rss: 0) },
            by: .cpu, limit: 1
        )
        XCTAssertEqual(go.first?.label, "go test ×3")
        let long = ProcCulprit(name: String(repeating: "x", count: 30), pid: 1, cmd: nil)
        XCTAssertEqual(long.label, String(repeating: "x", count: 23) + "…")
    }

    // MARK: - Folder

    private func proc(
        _ pid: Int32 = 1, _ name: String = "node", cmd: String? = nil, cpu: Double = 1,
        cwd: String? = nil, repo: String? = nil, parent: String? = nil, ppid: Int32? = nil, user: String? = nil
    ) -> ProcSample {
        ProcSample(pid: pid, ppid: ppid, name: name, cmd: cmd, user: user, cpuPct: cpu, rss: 0,
                   cwd: cwd, repo: repo, parent: parent)
    }

    func testMergesByNameAndFolder() {
        let items = [
            proc(1, repo: "/w/sling"), proc(2, repo: "/w/sling"),
            proc(3, repo: "/w/trebi"), proc(4, cwd: "/w/trebi/py", repo: "/w/trebi"),
        ]
        let top = ProcCulprit.top(items, by: .cpu, limit: 5)
        XCTAssertEqual(top.count, 2)
        XCTAssertEqual(Set(top.map(\.count)), [2])
        // The repo wins over the cwd in the key.
        XCTAssertEqual(Set(top.map(\.key.folder)), ["/w/sling", "/w/trebi"])
    }

    func testSameNameNoFolderMerges() {
        let top = ProcCulprit.top([proc(1), proc(2, cwd: ""), proc(3, repo: "")], by: .cpu, limit: 5)
        XCTAssertEqual(top.count, 1)
        XCTAssertEqual(top[0].count, 3)
        XCTAssertNil(top[0].key.folder)
    }

    func testFolderLabelWorktree() {
        let c = ProcCulprit(proc(repo: "/Users/u/sling-workspace/.wt/SLING-40/sling"))
        XCTAssertEqual(c.folderLabel, "sling (.wt/SLING-40)")
        // The suffix also applies when only the cwd gives the folder.
        XCTAssertEqual(ProcCulprit(proc(cwd: "/w/.wt/T-1/app/src")).folderLabel, "src (.wt/T-1)")
        // The task folder itself is the last segment: no suffix.
        XCTAssertEqual(ProcCulprit(proc(cwd: "/w/.wt/T-1")).folderLabel, "T-1")
    }

    func testFolderLabelNoRepoUsesCwd() {
        XCTAssertEqual(ProcCulprit(proc(cwd: "/home/u/work/api")).folderLabel, "api")
        XCTAssertEqual(ProcCulprit(proc(cwd: "/srv/app/api", repo: "/srv/app")).folderLabel, "app")
        XCTAssertEqual(ProcCulprit(proc(repo: "C:\\Users\\u\\code\\app")).folderLabel, "app")
        XCTAssertNil(ProcCulprit(proc(cwd: "/")).folderLabel)
        XCTAssertNil(ProcCulprit(proc()).folderLabel)
    }

    func testPathPartsHomeAbbrev() {
        let c = ProcCulprit(proc(cwd: "/Users/u/Projects/towertail-workspace/server/internal/hub",
                                 repo: "/Users/u/Projects/towertail-workspace/towertail", user: "u"))
        XCTAssertEqual(c.pathParts, ProcCulprit.PathParts(
            prefix: "~/Projects/towertail-workspace/", repoName: "towertail", subPath: ""))
        let sub = ProcCulprit(proc(cwd: "/home/u/code/app/web/src", repo: "/home/u/code/app", user: "u"))
        XCTAssertEqual(sub.pathParts, ProcCulprit.PathParts(prefix: "~/code/", repoName: "app", subPath: "/web/src"))
        let win = ProcCulprit(proc(repo: "C:\\Users\\u\\code\\app", user: "u"))
        XCTAssertEqual(win.pathParts, ProcCulprit.PathParts(prefix: "~\\code\\", repoName: "app", subPath: ""))
        // Another user's home stays as it is. A cwd with no repo has no sub path.
        let other = ProcCulprit(proc(cwd: "/home/w/x/api", user: "u"))
        XCTAssertEqual(other.pathParts, ProcCulprit.PathParts(prefix: "/home/w/x/", repoName: "api", subPath: ""))
        XCTAssertNil(ProcCulprit(proc()).pathParts)
    }

    func testParentHiddenForInit() {
        XCTAssertEqual(ProcCulprit(proc(parent: "claude", ppid: 4242)).parentLabel, "claude")
        XCTAssertEqual(ProcCulprit(proc(parent: "claude")).parentLabel, "claude")
        XCTAssertNil(ProcCulprit(proc(parent: "systemd", ppid: 1)).parentLabel)
        XCTAssertNil(ProcCulprit(proc(parent: "claude", ppid: 1)).parentLabel)
        for init0 in ["launchd", "systemd", "init", "Systemd"] {
            XCTAssertNil(ProcCulprit(proc(parent: init0, ppid: 500)).parentLabel, init0)
        }
        XCTAssertNil(ProcCulprit(proc(parent: "")).parentLabel)
        XCTAssertNil(ProcCulprit(proc()).parentLabel)
    }

    func testLabelWithFolder() {
        let items = (1...4).map {
            proc(Int32($0), cmd: "node /p/node_modules/.bin/vitest run", cpu: 10,
                 repo: "/w/.wt/SLING-40/sling", parent: "claude")
        }
        let top = ProcCulprit.top(items, by: .cpu, limit: 1)
        XCTAssertEqual(top[0].label, "node ×4 · sling (.wt/SLING-40) · vitest run")
        XCTAssertEqual(top[0].title, "node ×4")
        XCTAssertEqual(top[0].detail, "vitest run")
        XCTAssertEqual(top[0].parent, "claude")
        // A what that starts with the name replaces the name.
        XCTAssertNil(ProcCulprit(proc(1, "go", cmd: "go test ./...")).detail)
        XCTAssertEqual(ProcCulprit(proc(1, "go", cmd: "go test ./...", repo: "/srv/app")).label, "go test · app")
    }

    func testIORankingSumsRates() {
        let mb = 1_048_576.0
        let rates: [Int32: ProcIORate] = [
            1: ProcIORate(read: 1 * mb, write: 20 * mb),
            2: ProcIORate(read: 2 * mb, write: 1 * mb),
            3: ProcIORate(read: 0.5 * mb, write: 0),
        ]
        let items = [proc(1, "duckdb"), proc(2, "duckdb"), proc(3, "cron"), proc(4, "idle")]
        let ranking = ProcCulprit.Ranking.io(rates: rates)
        let top = ProcCulprit.top(items, by: ranking, limit: 3)
        XCTAssertEqual(top.map(\.name), ["duckdb", "cron", "idle"])
        XCTAssertEqual(ranking.text(top[0]), "R 3.0 · W 21")
        XCTAssertEqual(ranking.text(top[1]), "R 0.5 · W 0.0")
        XCTAssertEqual(ranking.value(top[2]), 0)
    }

    // MARK: - Shared with the server

    private struct Case: Decodable {
        let name, cmd, cwd, repo: String
        let pid: Int32
        let what, folder, label: String
    }

    /// The server alerter reads the same table (`server/internal/alerter/testdata/culprit_cases.json`).
    func testSharedCaseTable() throws {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appendingPathComponent("../../../server/internal/alerter/testdata/culprit_cases.json")
            .standardized
        let cases = try JSONDecoder().decode([Case].self, from: Data(contentsOf: url))
        XCTAssertGreaterThanOrEqual(cases.count, 30)
        for (i, tc) in cases.enumerated() {
            let msg = "case \(i): \(tc.name) \(tc.cmd)"
            XCTAssertEqual(ProcCulprit.what(name: tc.name, cmd: tc.cmd) ?? "", tc.what, msg)
            let c = ProcCulprit(proc(tc.pid, tc.name, cmd: tc.cmd, cwd: tc.cwd, repo: tc.repo))
            XCTAssertEqual(c.folderLabel ?? "", tc.folder, msg)
            XCTAssertEqual(c.label, tc.label, msg)
        }
    }
}
