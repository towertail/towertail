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
        // Give the async append queue a moment to flush.
        try await Task.sleep(nanoseconds: 200_000_000)

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
        try await Task.sleep(nanoseconds: 200_000_000)

        let la = store.loadRecent(nodeID: a)
        let lb = store.loadRecent(nodeID: b)
        XCTAssertEqual(la.count, 1)
        XCTAssertEqual(lb.count, 1)
        XCTAssertEqual(la.first?.cpu ?? 0, 0.1, accuracy: 0.0001)
        XCTAssertEqual(lb.first?.cpu ?? 0, 0.9, accuracy: 0.0001)
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
