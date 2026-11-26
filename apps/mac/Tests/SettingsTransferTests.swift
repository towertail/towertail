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

    private func makeExport(nodes: [Node], cpuWarn: Double = 0.9,
                            tweak: (inout PersistedSettings) -> Void = { _ in }) -> SettingsExport {
        var p = base(nodes: nodes)
        p.thresholds.cpuWarn = cpuWarn
        p.localPollingIntervalSeconds = 5
        p.cardDensity = "b"
        p.notificationsEnabled = true
        p.defaultTerminalApp = "iTerm"
        p.menuBarIconStyle = "full"
        tweak(&p)
        return SettingsExport.from(p, appVersion: "0.9.9")
    }

    func testRoundTripCarriesAllFields() throws {
        let node = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db.internal",
                        customLongProc: .defaults, ignoredProcs: ["postgres"], mutedSignals: MutedSignals([.zombies]))
        let export = makeExport(nodes: [node]) { p in
            p.popoverLayout = "cards"
            p.longProc.cpuPct = 120
            p.longProc.ignore = ["clickhouse-server"]
            p.thresholds.muted = MutedSignals([.cpu])
        }
        XCTAssertEqual(export.general.popoverLayout, "cards")
        let back = try SettingsTransfer.decode(SettingsTransfer.encode(export))
        // iso8601 drops sub-second precision, so compare the rest piecewise.
        XCTAssertEqual(back.version, export.version)
        XCTAssertEqual(back.appVersion, export.appVersion)
        XCTAssertEqual(back.general, export.general)
        XCTAssertEqual(back.globalThresholds, export.globalThresholds)
        XCTAssertEqual(back.notifications, export.notifications)
        XCTAssertEqual(back.nodes, export.nodes)
        XCTAssertLessThan(abs(back.exportedAt.timeIntervalSince(export.exportedAt)), 1.0)
        XCTAssertEqual(back.longProc, export.longProc)
        XCTAssertEqual(back.longProc?.cpuPct, 120)
        XCTAssertEqual(back.longProc?.ignore, ["clickhouse-server"])
        XCTAssertEqual(back.nodes.first?.ignoredProcs, ["postgres"])
        XCTAssertEqual(back.nodes.first?.customLongProc, .defaults)
        XCTAssertEqual(back.globalThresholds.muted, MutedSignals([.cpu]))
        XCTAssertEqual(back.nodes.first?.mutedSignals, MutedSignals([.zombies]))
        let (merged, _) = SettingsTransfer.apply(back, to: base(), selection: .allDefaults)
        XCTAssertEqual(merged.popoverLayout, "cards")
        XCTAssertEqual(merged.longProc.cpuPct, 120)
    }

    func testImportWithoutFieldKeepsCurrent() throws {
        let cases: [(name: String, current: (inout PersistedSettings) -> Void, remove: [String],
                     check: (SettingsExport, PersistedSettings, String) -> Void)] = [
            ("menuBarIconStyle", { $0.menuBarIconStyle = "full" }, ["general", "menuBarIconStyle"],
             { _, merged, msg in XCTAssertEqual(merged.menuBarIconStyle, "full", msg) }),
            ("popoverLayout", { $0.popoverLayout = "cards" }, ["general", "popoverLayout"],
             { _, merged, msg in XCTAssertEqual(merged.popoverLayout, "cards", msg) }),
            ("longProc", { $0.longProc.cpuPct = 200 }, ["longProc"],
             { ex, merged, msg in
                 XCTAssertNil(ex.longProc, msg)
                 XCTAssertEqual(merged.longProc.cpuPct, 200, msg)
             }),
            ("muted", { $0.thresholds.muted = MutedSignals([.memPressure]) }, ["globalThresholds", "muted"],
             { ex, merged, msg in
                 XCTAssertNil(ex.globalThresholds.muted, msg)
                 XCTAssertEqual(merged.thresholds.muted, MutedSignals([.memPressure]), msg)
             }),
        ]
        for c in cases {
            var current = base()
            c.current(&current)
            var json = try XCTUnwrap(JSONSerialization.jsonObject(with: SettingsTransfer.encode(makeExport(nodes: []))) as? [String: Any])
            if c.remove.count == 1 {
                json[c.remove[0]] = nil
            } else {
                var section = try XCTUnwrap(json[c.remove[0]] as? [String: Any])
                section[c.remove[1]] = nil
                json[c.remove[0]] = section
            }
            let ex = try SettingsTransfer.decode(JSONSerialization.data(withJSONObject: json))
            var sel = ImportSelection.allDefaults
            sel.globalThresholds = true
            c.check(ex, SettingsTransfer.apply(ex, to: current, selection: sel).merged, c.name)
        }
    }

    func testPersistedSettingsMissingKeyUsesDefault() throws {
        let cases: [(name: String, change: (inout [String: Any]) -> Void, check: (PersistedSettings, String) -> Void)] = [
            ("menuBarIconStyle", { $0["platform"] = ["darwin": ["launchAtLogin": false]] },
             { XCTAssertEqual($0.menuBarIconStyle, "pulse", $1) }),
            ("popoverLayout", { $0.removeValue(forKey: "popoverLayout") },
             { XCTAssertEqual($0.popoverLayout, "rows", $1) }),
            ("longProc", { $0["longProc"] = nil },
             {
                 XCTAssertEqual($0.longProc, .defaults, $1)
                 XCTAssertNil($0.nodes.first?.customLongProc, $1)
                 XCTAssertEqual($0.nodes.first?.ignoredProcs, [], $1)
             }),
        ]
        for c in cases {
            var json = try XCTUnwrap(JSONSerialization.jsonObject(
                with: JSONEncoder().encode(base(nodes: [Node(displayName: "db", kind: .ssh)]))) as? [String: Any])
            c.change(&json)
            let data = try JSONSerialization.data(withJSONObject: json)
            c.check(try JSONDecoder().decode(PersistedSettings.self, from: data), c.name)
        }
    }

    func testImportServerThresholdsCopiesOverrideFields() {
        let existing = Node(displayName: "db", kind: .ssh, sshUser: "ops", sshHost: "db")
        var incoming = existing
        incoming.thresholdOverrides = ThresholdOverrides(cpu: ThresholdPair(warn: 0.3, critical: 0.6))
        incoming.customLongProc = .defaults
        incoming.ignoredProcs = ["postgres"]
        incoming.mutedSignals = MutedSignals([.cpu])
        var sel = ImportSelection.allDefaults
        sel.servers = false          // don't add/replace hosts
        sel.serverThresholds = true  // but do sync custom overrides
        let (merged, report) = SettingsTransfer.apply(makeExport(nodes: [incoming]), to: base(nodes: [existing]), selection: sel)
        XCTAssertEqual(merged.nodes.count, 1)
        XCTAssertEqual(merged.nodes.first?.thresholdOverrides?.cpu?.warn, 0.3)
        XCTAssertEqual(merged.nodes.first?.customLongProc, .defaults)
        XCTAssertEqual(merged.nodes.first?.ignoredProcs, ["postgres"])
        XCTAssertEqual(merged.nodes.first?.mutedSignals, MutedSignals([.cpu]))
        XCTAssertEqual(report.serverThresholdsUpdated, 1)
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
        XCTAssertEqual(merged.menuBarIconStyle, "full")
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

    func testMenuBarIconStyleLivesInDarwinBlock() throws {
        var b = base()
        b.menuBarIconStyle = "full"
        let data = try JSONEncoder().encode(b)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let darwin = try XCTUnwrap((json["platform"] as? [String: Any])?["darwin"] as? [String: Any])
        XCTAssertEqual(darwin["menuBarIconStyle"] as? String, "full")
        XCTAssertEqual(try JSONDecoder().decode(PersistedSettings.self, from: data).menuBarIconStyle, "full")
    }

    private func decodeLayout(version: Any?, layout: String) throws -> String {
        var b = base()
        b.popoverLayout = layout
        var json = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(b)) as? [String: Any])
        if let version { json["schemaVersion"] = version } else { json.removeValue(forKey: "schemaVersion") }
        let data = try JSONSerialization.data(withJSONObject: json)
        return try JSONDecoder().decode(PersistedSettings.self, from: data).popoverLayout
    }

    func testVersion1FileMovesToRows() throws {
        XCTAssertEqual(try decodeLayout(version: 1, layout: "cards"), "rows")
    }

    func testNoVersionMovesToRows() throws {
        XCTAssertEqual(try decodeLayout(version: nil, layout: "cards"), "rows")
    }

    func testVersion2KeepsCards() throws {
        XCTAssertEqual(try decodeLayout(version: 2, layout: "cards"), "cards")
    }

    func testEncodeWritesVersion2() throws {
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(base())) as? [String: Any])
        XCTAssertEqual(json["schemaVersion"] as? Int, 2)
    }

    func testBadSchemaVersionKeepsSettings() throws {
        var b = base()
        b.cardDensity = "b"
        var json = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(b)) as? [String: Any])
        json["schemaVersion"] = "two"
        let data = try JSONSerialization.data(withJSONObject: json)
        let decoded = try JSONDecoder().decode(PersistedSettings.self, from: data)
        XCTAssertEqual(decoded.cardDensity, "b")
        XCTAssertEqual(decoded.popoverLayout, "rows")
    }

    @MainActor
    func testClientSettingsPickCardsSurvivesReload() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tt-layout-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let url = dir.appendingPathComponent("settings.json")
        var b = base()
        b.popoverLayout = "cards"
        var json = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(b)) as? [String: Any])
        json["schemaVersion"] = 1
        try JSONSerialization.data(withJSONObject: json).write(to: url)

        let settings = ClientSettings(url: url)
        XCTAssertEqual(settings.popoverLayout, .rows)
        settings.popoverLayout = .cards
        settings.persist()
        XCTAssertEqual(ClientSettings(url: url).popoverLayout, .cards)
    }

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

    func testBadMutedValueKeepsSettings() throws {
        var json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(base())) as! [String: Any]
        var t = json["thresholds"] as! [String: Any]
        t["muted"] = 42
        json["thresholds"] = t
        let p = try JSONDecoder().decode(PersistedSettings.self, from: JSONSerialization.data(withJSONObject: json))
        XCTAssertNil(p.thresholds.muted)
        XCTAssertEqual(p.thresholds.cpuWarn, 0.5)
    }
}
