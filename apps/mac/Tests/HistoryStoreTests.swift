import SQLite3
import XCTest
@testable import Towertail

final class HistoryStoreTests: XCTestCase {
    private func tempURL() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("towertail-history-\(UUID().uuidString).sqlite")
    }

    func testAppendAndLoadRecent() async throws {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = HistoryStore(url: url)
        let id = UUID()
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<5 {
            store.append(nodeID: id, point: HistoryPoint(
                t: t0.addingTimeInterval(Double(i)),
                cpu: Double(i) / 10.0, mem: 0.2, disk: 0.3, net: 0.05,
                rxMBps: 1.0, txMBps: 0.5
            ))
        }

        let loaded = store.loadRecent(nodeID: id)
        XCTAssertEqual(loaded.count, 5)
        XCTAssertEqual(loaded.first?.cpu ?? -1, 0.0, accuracy: 0.0001)
        XCTAssertEqual(loaded.last?.cpu ?? 0, 0.4, accuracy: 0.0001)
    }

    func testSeparateNodesDoNotCollide() async throws {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = HistoryStore(url: url)
        let a = UUID(), b = UUID()
        let t = Date()
        store.append(nodeID: a, point: HistoryPoint(t: t, cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil))
        store.append(nodeID: b, point: HistoryPoint(t: t, cpu: 0.9, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil))

        let la = store.loadRecent(nodeID: a)
        let lb = store.loadRecent(nodeID: b)
        XCTAssertEqual(la.count, 1)
        XCTAssertEqual(lb.count, 1)
        XCTAssertEqual(la.first?.cpu ?? 0, 0.1, accuracy: 0.0001)
        XCTAssertEqual(lb.first?.cpu ?? 0, 0.9, accuracy: 0.0001)
    }

    func testLoadRangeAveragesToMaxPoints() async throws {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = HistoryStore(url: url)
        let id = UUID()
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        for i in 0..<100 {
            store.append(nodeID: id, point: HistoryPoint(
                t: t0.addingTimeInterval(Double(i)), cpu: Double(i) / 100, mem: 0.2, disk: 0.3, net: nil,
                rxMBps: nil, txMBps: nil, score: Double(i), load: 2
            ))
        }

        let pts = store.loadRange(nodeID: id, from: t0, to: t0.addingTimeInterval(100), maxPoints: 10)
        XCTAssertEqual(pts.count, 10)
        XCTAssertEqual(pts[0].cpu ?? -1, 0.045, accuracy: 0.001)
        XCTAssertEqual(pts[9].score ?? -1, 94.5, accuracy: 0.01)
        XCTAssertEqual(pts[3].load ?? -1, 2, accuracy: 0.0001)
        XCTAssertEqual(pts.map(\.t), pts.map(\.t).sorted())

        let half = store.loadRange(nodeID: id, from: t0.addingTimeInterval(50), to: t0.addingTimeInterval(100), maxPoints: 5)
        XCTAssertEqual(half.count, 5)
        XCTAssertGreaterThan(half[0].t, t0.addingTimeInterval(49))
        XCTAssertTrue(store.loadRange(nodeID: id, from: t0, to: t0, maxPoints: 5).isEmpty)
        XCTAssertTrue(store.loadRange(nodeID: UUID(), from: t0, to: t0.addingTimeInterval(100), maxPoints: 5).isEmpty)
    }

    func testRecentKeepsScoreAndLoad() async throws {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        let store = HistoryStore(url: url)
        let id = UUID()
        store.append(nodeID: id, point: HistoryPoint(
            t: Date(), cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil, score: 77, load: 1.25
        ))
        let p = try XCTUnwrap(store.loadRecent(nodeID: id).first)
        XCTAssertEqual(p.score, 77)
        XCTAssertEqual(p.load, 1.25)
    }

    func testMigrationAddsScoreAndLoadColumns() async throws {
        let url = tempURL()
        defer { try? FileManager.default.removeItem(at: url) }
        // A store of an older version: no procs, health_score or load_1 column.
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(url.path, &db), SQLITE_OK)
        let old = """
        CREATE TABLE samples (
            node_id TEXT NOT NULL, ts REAL NOT NULL,
            cpu REAL, mem REAL, disk REAL, net REAL, rx_mbps REAL, tx_mbps REAL
        );
        INSERT INTO samples VALUES ('\(UUID().uuidString)', 1700000000, 0.5, 0.5, 0.5, 0, 0, 0);
        """
        XCTAssertEqual(sqlite3_exec(db, old, nil, nil, nil), SQLITE_OK)
        sqlite3_close(db)

        let store = HistoryStore(url: url)
        let id = UUID()
        store.append(nodeID: id, point: HistoryPoint(
            t: Date(), cpu: 0.1, mem: nil, disk: nil, net: nil, rxMBps: nil, txMBps: nil, score: 60, load: 3
        ))
        let p = try XCTUnwrap(store.loadRecent(nodeID: id).first)
        XCTAssertEqual(p.score, 60)
        XCTAssertEqual(p.load, 3)
    }
}

final class SettingsMigrationTests: XCTestCase {
    func testLegacyPollingFieldMigratesToBothKinds() throws {
        let legacyJSON = """
        {
            "nodes": [],
            "thresholds": {
                "cpuWarn": 0.75, "cpuCritical": 0.9,
                "memWarn": 0.75, "memCritical": 0.9,
                "diskWarn": 0.85, "diskCritical": 0.95
            },
            "pollingIntervalSeconds": 7,
            "cardDensity": "a",
            "notificationsEnabled": false,
            "notifyWarn": true,
            "notifyCritical": true,
            "notifyDebounceSeconds": 60,
            "launchAtLogin": false
        }
        """.data(using: .utf8)!
        let decoded = try JSONDecoder().decode(PersistedSettings.self, from: legacyJSON)
        XCTAssertEqual(decoded.localPollingIntervalSeconds, 7)
        XCTAssertEqual(decoded.sshPollingIntervalSeconds, 7)
    }

    func testNewFieldsTakePrecedenceOverLegacy() throws {
        let json = """
        {
            "nodes": [],
            "thresholds": {
                "cpuWarn": 0.75, "cpuCritical": 0.9,
                "memWarn": 0.75, "memCritical": 0.9,
                "diskWarn": 0.85, "diskCritical": 0.95
            },
            "pollingIntervalSeconds": 7,
            "localPollingIntervalSeconds": 2,
            "sshPollingIntervalSeconds": 10,
            "cardDensity": "a",
            "notificationsEnabled": false,
            "notifyWarn": true,
            "notifyCritical": true,
            "notifyDebounceSeconds": 60,
            "launchAtLogin": false
        }
        """.data(using: .utf8)!
        let decoded = try JSONDecoder().decode(PersistedSettings.self, from: json)
        XCTAssertEqual(decoded.localPollingIntervalSeconds, 2)
        XCTAssertEqual(decoded.sshPollingIntervalSeconds, 10)
    }
}
