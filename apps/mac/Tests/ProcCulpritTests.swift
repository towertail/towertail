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
}
