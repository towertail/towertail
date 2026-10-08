import XCTest
@testable import Towertail

final class SettingsTransferTests: XCTestCase {
    private func base(nodes: [Node] = []) -> PersistedSettings {
        PersistedSettings(
            nodes: nodes,
            thresholds: PersistedThresholds(
                cpuWarn: 0.5, cpuCritical: 0.8,
                memWarn: 0.5, memCritical: 0.8,
                diskWarn: 0.5, diskCritical: 0.8
            ),
            localPollingIntervalSeconds: 2,
            sshPollingIntervalSeconds: 10,
            cardDensity: "a",
            notificationsEnabled: false,
            notifyWarn: true,
            notifyCritical: true,
            notifyDebounceSeconds: 60,
            launchAtLogin: false,
            autoUpdateSamplersEnabled: false,
            defaultTerminalApp: "Terminal",
            postWakeGraceSeconds: 15
        )
    }

    private func makeExport(nodes: [Node], cpuWarn: Double = 0.9) -> SettingsExport {
        var p = base(nodes: nodes)
        p.thresholds.cpuWarn = cpuWarn
        p.localPollingIntervalSeconds = 5
        p.cardDensity = "b"
        p.notificationsEnabled = true
        p.defaultTerminalApp = "iTerm"
        return SettingsExport.from(p, appVersion: "0.9.9")
    }

    func testRoundTripEncodeDecode() throws {
        let export = makeExport(nodes: [
            Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db.internal"),
        ])
        let data = try SettingsTransfer.encode(export)
        let back = try SettingsTransfer.decode(data)
        // iso8601 drops sub-second precision, so compare the rest piecewise.
        XCTAssertEqual(back.version, export.version)
        XCTAssertEqual(back.appVersion, export.appVersion)
        XCTAssertEqual(back.general, export.general)
        XCTAssertEqual(back.globalThresholds, export.globalThresholds)
        XCTAssertEqual(back.notifications, export.notifications)
        XCTAssertEqual(back.nodes, export.nodes)
        XCTAssertLessThan(abs(back.exportedAt.timeIntervalSince(export.exportedAt)), 1.0)
    }

    func testImportWithoutAlertsKeepsCurrentAlerts() throws {
        var b = base()
        b.thresholds.alerts.cpu = AlertRule(sustainSeconds: 900, notify: .off)
        let ex = makeExport(nodes: [])
        var json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(ex)) as! [String: Any]
        var t = json["globalThresholds"] as! [String: Any]
        t["alerts"] = nil
        t["cpuSustainSamples"] = 6
        json["globalThresholds"] = t
        let old = try JSONDecoder().decode(SettingsExport.self, from: JSONSerialization.data(withJSONObject: json))
        let (merged, _) = SettingsTransfer.apply(old, to: b, selection: .allDefaults)
        XCTAssertEqual(merged.thresholds.cpuWarn, 0.9)
        XCTAssertEqual(merged.thresholds.alerts.cpu, AlertRule(sustainSeconds: 900, notify: .off))
        XCTAssertNil(merged.thresholds.legacySustain)
    }

    func testRejectsFutureVersion() throws {
        var export = makeExport(nodes: [])
        export.version = SettingsExport.currentVersion + 99
        let data = try SettingsTransfer.encode(export)
        XCTAssertThrowsError(try SettingsTransfer.decode(data))
    }

    func testApplyGeneralOnly() {
        let b = base()
        let ex = makeExport(nodes: [])
        var sel = ImportSelection.allDefaults
        sel.globalThresholds = false
        sel.notifications = false
        sel.servers = false
        sel.serverThresholds = false
        let (merged, report) = SettingsTransfer.apply(ex, to: b, selection: sel)
        XCTAssertEqual(merged.localPollingIntervalSeconds, 5)
        XCTAssertEqual(merged.cardDensity, "b")
        XCTAssertEqual(merged.defaultTerminalApp, "iTerm")
        XCTAssertEqual(merged.thresholds.cpuWarn, 0.5, "global thresholds should not have changed")
        XCTAssertFalse(merged.notificationsEnabled, "notifications section was unticked")
        XCTAssertTrue(report.generalApplied)
        XCTAssertFalse(report.globalThresholdsApplied)
    }

    func testMergeServersAddsNew() {
        let existing = Node(displayName: "mac", kind: .local)
        let incoming = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        let b = base(nodes: [existing])
        let ex = makeExport(nodes: [incoming])
        var sel = ImportSelection.allDefaults
        sel.serverStrategy = .merge
        let (merged, report) = SettingsTransfer.apply(ex, to: b, selection: sel)
        XCTAssertEqual(merged.nodes.count, 2)
        XCTAssertTrue(merged.nodes.contains(where: { $0.displayName == "mac" }))
        XCTAssertTrue(merged.nodes.contains(where: { $0.displayName == "db" }))
        XCTAssertEqual(report.serversAdded, 1)
        XCTAssertEqual(report.serversRemoved, 0)
    }

    func testMergeUpdatesMatchingById() {
        var existing = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        existing.tags = ["old"]
        var incoming = existing
        incoming.tags = ["new"]
        incoming.displayName = "db-prod"
        let b = base(nodes: [existing])
        let ex = makeExport(nodes: [incoming])
        let (merged, report) = SettingsTransfer.apply(ex, to: b, selection: .allDefaults)
        XCTAssertEqual(merged.nodes.count, 1)
        XCTAssertEqual(merged.nodes.first?.displayName, "db-prod")
        XCTAssertEqual(merged.nodes.first?.tags, ["new"])
        XCTAssertEqual(report.serversUpdated, 1)
    }

    func testMergePreservesIdWhenMatchedByHostTuple() {
        // Same host tuple but different UUIDs — this is the "I set up the
        // same server by hand on both Macs" case. History is keyed on the
        // existing id, so merge must keep it rather than swap in the
        // imported id.
        let existing = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        let incoming = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db", tags: ["imported"])
        XCTAssertNotEqual(existing.id, incoming.id)
        let b = base(nodes: [existing])
        let ex = makeExport(nodes: [incoming])
        let (merged, _) = SettingsTransfer.apply(ex, to: b, selection: .allDefaults)
        XCTAssertEqual(merged.nodes.count, 1)
        XCTAssertEqual(merged.nodes.first?.id, existing.id)
        XCTAssertEqual(merged.nodes.first?.tags, ["imported"])
    }

    func testOverwriteReplacesEntireList() {
        let keep = Node(displayName: "mac", kind: .local)
        let replace = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        let b = base(nodes: [keep, Node(displayName: "stale", kind: .ssh, sshUser: "u", sshHost: "h")])
        let ex = makeExport(nodes: [keep, replace])
        var sel = ImportSelection.allDefaults
        sel.serverStrategy = .overwrite
        let (merged, report) = SettingsTransfer.apply(ex, to: b, selection: sel)
        XCTAssertEqual(merged.nodes.count, 2)
        XCTAssertFalse(merged.nodes.contains(where: { $0.displayName == "stale" }))
        XCTAssertEqual(report.serversRemoved, 1)
    }

    // --- Cross-OS round-trip -----------------------------------------------
    //
    // The Windows client writes a `platform.windows` block into settings.json.
    // An export from Mac must carry that block through unchanged so a user
    // who round-trips Mac → Windows → Mac (or vice-versa) never loses their
    // Windows-only settings.

    func testExportCarriesSourcePlatform() {
        let ex = makeExport(nodes: [])
        XCTAssertEqual(ex.sourcePlatform, "darwin")
    }

    func testExportCarriesForeignPlatformBlocks() throws {
        var b = base()
        b.platformBlocks.foreign["windows"] = .object([
            "launchAtStartup": .bool(true),
            "defaultTerminalApp": .string("WindowsTerminal"),
        ])
        let ex = SettingsExport.from(b)
        XCTAssertEqual(ex.platformBlocks?["windows"], .object([
            "launchAtStartup": .bool(true),
            "defaultTerminalApp": .string("WindowsTerminal"),
        ]))
        // Round-trip through JSON to catch silent drop of the new field.
        let data = try SettingsTransfer.encode(ex)
        let back = try SettingsTransfer.decode(data)
        XCTAssertEqual(back.platformBlocks?["windows"], ex.platformBlocks?["windows"])
        XCTAssertEqual(back.sourcePlatform, "darwin")
    }

    func testImportPreservesExistingForeignBlocksWhenImportHasNone() {
        var existing = base()
        existing.platformBlocks.foreign["windows"] = .object([
            "launchAtStartup": .bool(true)
        ])
        var ex = makeExport(nodes: [])
        ex.platformBlocks = nil
        let (merged, _) = SettingsTransfer.apply(ex, to: existing, selection: .allDefaults)
        XCTAssertEqual(merged.platformBlocks.foreign["windows"], .object([
            "launchAtStartup": .bool(true)
        ]))
    }

    func testImportMergesForeignBlocksFromExport() {
        var existing = base()
        existing.platformBlocks.foreign["linux"] = .object(["x": .bool(false)])
        var ex = makeExport(nodes: [])
        ex.platformBlocks = [
            "windows": .object(["launchAtStartup": .bool(true)])
        ]
        let (merged, _) = SettingsTransfer.apply(ex, to: existing, selection: .allDefaults)
        // Windows block from import applied; Linux block from existing preserved.
        XCTAssertEqual(merged.platformBlocks.foreign["windows"], .object([
            "launchAtStartup": .bool(true)
        ]))
        XCTAssertEqual(merged.platformBlocks.foreign["linux"], .object(["x": .bool(false)]))
    }

    func testImportServerThresholdsOnlyAppliesOverrides() {
        let existingID = UUID()
        let existing = Node(id: existingID, displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        var withThresh = existing
        withThresh.customThresholds = MetricThresholds(
            cpuWarn: 0.3, cpuCritical: 0.6,
            memWarn: 0.3, memCritical: 0.6,
            diskWarn: 0.3, diskCritical: 0.6
        )
        let b = base(nodes: [existing])
        let ex = makeExport(nodes: [withThresh])
        var sel = ImportSelection.allDefaults
        sel.servers = false          // don't add/replace hosts
        sel.serverThresholds = true  // but do sync custom overrides
        let (merged, report) = SettingsTransfer.apply(ex, to: b, selection: sel)
        XCTAssertEqual(merged.nodes.count, 1)
        XCTAssertNotNil(merged.nodes.first?.customThresholds)
        XCTAssertEqual(merged.nodes.first?.customThresholds?.cpuWarn, 0.3)
        XCTAssertEqual(report.serverThresholdsUpdated, 1)
    }
}
