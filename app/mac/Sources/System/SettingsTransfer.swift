import Foundation

/// Export envelope written to disk when the user picks "Export settings…".
/// The shape is deliberately a superset of `PersistedSettings` so future
/// additions can land by extending both types in lockstep — the import
/// code only copies over the sections the user ticked, so unknown fields
/// on older builds are simply ignored rather than stomping state.
struct SettingsExport: Codable, Equatable {
    static let currentVersion = 1

    var version: Int
    var exportedAt: Date
    var appVersion: String?

    var general: GeneralSection
    var globalThresholds: PersistedThresholds
    var notifications: NotificationsSection
    var nodes: [Node]

    struct GeneralSection: Codable, Equatable {
        var localPollingIntervalSeconds: Int
        var sshPollingIntervalSeconds: Int
        var cardDensity: String
        var launchAtLogin: Bool
        var autoUpdateSamplersEnabled: Bool
        var defaultTerminalApp: String
        var postWakeGraceSeconds: Int?

        enum CodingKeys: String, CodingKey {
            case localPollingIntervalSeconds, sshPollingIntervalSeconds
            case cardDensity, launchAtLogin, autoUpdateSamplersEnabled
            case defaultTerminalApp, postWakeGraceSeconds
        }
    }

    struct NotificationsSection: Codable, Equatable {
        var notificationsEnabled: Bool
        var notifyWarn: Bool
        var notifyCritical: Bool
        var notifyDebounceSeconds: Int
    }

    static func from(_ p: PersistedSettings, appVersion: String? = nil) -> SettingsExport {
        SettingsExport(
            version: currentVersion,
            exportedAt: Date(),
            appVersion: appVersion,
            general: GeneralSection(
                localPollingIntervalSeconds: p.localPollingIntervalSeconds,
                sshPollingIntervalSeconds: p.sshPollingIntervalSeconds,
                cardDensity: p.cardDensity,
                launchAtLogin: p.launchAtLogin,
                autoUpdateSamplersEnabled: p.autoUpdateSamplersEnabled,
                defaultTerminalApp: p.defaultTerminalApp,
                postWakeGraceSeconds: p.postWakeGraceSeconds
            ),
            globalThresholds: p.thresholds,
            notifications: NotificationsSection(
                notificationsEnabled: p.notificationsEnabled,
                notifyWarn: p.notifyWarn,
                notifyCritical: p.notifyCritical,
                notifyDebounceSeconds: p.notifyDebounceSeconds
            ),
            nodes: p.nodes
        )
    }
}

/// Which top-level sections the user chose to import. `serverThresholds`
/// is independent of `servers` so a user can sync just per-node threshold
/// overrides without replacing host definitions (useful when the hosts
/// already exist on the target Mac with local SSH key config).
struct ImportSelection: Equatable {
    var general: Bool
    var globalThresholds: Bool
    var notifications: Bool
    var servers: Bool
    var serverThresholds: Bool
    var serverStrategy: ServerStrategy

    enum ServerStrategy: String, CaseIterable {
        case merge     // add new, update matching by id (or name), keep others
        case overwrite // replace entire list with the imported set
    }

    static let allDefaults = ImportSelection(
        general: true,
        globalThresholds: true,
        notifications: true,
        servers: true,
        serverThresholds: true,
        serverStrategy: .merge
    )
}

struct ImportApplyReport: Equatable {
    var generalApplied: Bool
    var globalThresholdsApplied: Bool
    var notificationsApplied: Bool
    var serversAdded: Int
    var serversUpdated: Int
    var serversRemoved: Int
    var serverThresholdsUpdated: Int

    var summary: String {
        var parts: [String] = []
        if generalApplied { parts.append("general") }
        if globalThresholdsApplied { parts.append("thresholds") }
        if notificationsApplied { parts.append("notifications") }
        if serversAdded + serversUpdated + serversRemoved > 0 {
            var s = "servers(+\(serversAdded)"
            if serversUpdated > 0 { s += " ~\(serversUpdated)" }
            if serversRemoved > 0 { s += " -\(serversRemoved)" }
            s += ")"
            parts.append(s)
        }
        if serverThresholdsUpdated > 0 {
            parts.append("server-thresholds(\(serverThresholdsUpdated))")
        }
        return parts.isEmpty ? "no changes" : parts.joined(separator: ", ")
    }
}

enum SettingsTransferError: LocalizedError {
    case unreadable(String)
    case unsupportedVersion(Int)
    case malformed(String)

    var errorDescription: String? {
        switch self {
        case .unreadable(let m): return "Couldn't read file: \(m)"
        case .unsupportedVersion(let v): return "Unsupported settings version: \(v)"
        case .malformed(let m): return "File isn't a valid Towertail settings export: \(m)"
        }
    }
}

enum SettingsTransfer {
    static func encode(_ export: SettingsExport) throws -> Data {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys]
        enc.dateEncodingStrategy = .iso8601
        return try enc.encode(export)
    }

    static func decode(_ data: Data) throws -> SettingsExport {
        let dec = JSONDecoder()
        dec.dateDecodingStrategy = .iso8601
        do {
            let export = try dec.decode(SettingsExport.self, from: data)
            if export.version > SettingsExport.currentVersion {
                throw SettingsTransferError.unsupportedVersion(export.version)
            }
            return export
        } catch let e as SettingsTransferError {
            throw e
        } catch {
            throw SettingsTransferError.malformed(error.localizedDescription)
        }
    }

    /// Folds the selected sections of `imported` into `base` and returns
    /// the merged PersistedSettings plus a report of what changed. Pure —
    /// no disk I/O, no AppKit — so it can be unit-tested and the caller
    /// decides when to persist.
    static func apply(
        _ imported: SettingsExport,
        to base: PersistedSettings,
        selection: ImportSelection
    ) -> (merged: PersistedSettings, report: ImportApplyReport) {
        var out = base
        var report = ImportApplyReport(
            generalApplied: false,
            globalThresholdsApplied: false,
            notificationsApplied: false,
            serversAdded: 0,
            serversUpdated: 0,
            serversRemoved: 0,
            serverThresholdsUpdated: 0
        )

        if selection.general {
            out.localPollingIntervalSeconds = imported.general.localPollingIntervalSeconds
            out.sshPollingIntervalSeconds = imported.general.sshPollingIntervalSeconds
            out.cardDensity = imported.general.cardDensity
            out.launchAtLogin = imported.general.launchAtLogin
            out.autoUpdateSamplersEnabled = imported.general.autoUpdateSamplersEnabled
            out.defaultTerminalApp = imported.general.defaultTerminalApp
            if let grace = imported.general.postWakeGraceSeconds {
                out.postWakeGraceSeconds = grace
            }
            report.generalApplied = true
        }

        if selection.globalThresholds {
            out.thresholds = imported.globalThresholds
            report.globalThresholdsApplied = true
        }

        if selection.notifications {
            out.notificationsEnabled = imported.notifications.notificationsEnabled
            out.notifyWarn = imported.notifications.notifyWarn
            out.notifyCritical = imported.notifications.notifyCritical
            out.notifyDebounceSeconds = imported.notifications.notifyDebounceSeconds
            report.notificationsApplied = true
        }

        if selection.servers {
            let merge = mergeServers(
                existing: out.nodes,
                incoming: imported.nodes,
                strategy: selection.serverStrategy
            )
            out.nodes = merge.nodes
            report.serversAdded = merge.added
            report.serversUpdated = merge.updated
            report.serversRemoved = merge.removed
        } else if selection.serverThresholds {
            // Apply just the per-node threshold overrides onto matching
            // existing nodes. Match by id first, then by displayName so
            // users who hand-created the same host on both Macs still get
            // their thresholds synced.
            var updated = 0
            var byId = Dictionary(uniqueKeysWithValues: out.nodes.enumerated().map { ($1.id, $0) })
            var byName = Dictionary(out.nodes.enumerated().map { ($1.displayName, $0) }, uniquingKeysWith: { a, _ in a })
            for inc in imported.nodes {
                let idx = byId[inc.id] ?? byName[inc.displayName]
                guard let i = idx else { continue }
                if out.nodes[i].customThresholds != inc.customThresholds {
                    out.nodes[i].customThresholds = inc.customThresholds
                    updated += 1
                }
                // Reindex in case displayName was the match path and
                // the id differed — subsequent lookups should still work.
                byId[out.nodes[i].id] = i
                byName[out.nodes[i].displayName] = i
            }
            report.serverThresholdsUpdated = updated
        }

        return (out, report)
    }

    private struct MergeResult {
        var nodes: [Node]
        var added: Int
        var updated: Int
        var removed: Int
    }

    private static func mergeServers(
        existing: [Node],
        incoming: [Node],
        strategy: ImportSelection.ServerStrategy
    ) -> MergeResult {
        switch strategy {
        case .overwrite:
            let removed = existing.filter { e in !incoming.contains(where: { $0.id == e.id }) }.count
            let added = incoming.filter { i in !existing.contains(where: { $0.id == i.id }) }.count
            let updated = incoming.count - added
            return MergeResult(nodes: incoming, added: added, updated: updated, removed: removed)
        case .merge:
            var out = existing
            var added = 0
            var updated = 0
            for inc in incoming {
                if let i = out.firstIndex(where: { $0.id == inc.id }) {
                    if out[i] != inc {
                        out[i] = inc
                        updated += 1
                    }
                } else if let i = out.firstIndex(where: {
                    $0.kind == inc.kind
                    && $0.displayName == inc.displayName
                    && $0.sshUser == inc.sshUser
                    && $0.sshHost == inc.sshHost
                }) {
                    // Same host by identity tuple — overwrite in place but
                    // keep the existing id so history rows stay joined.
                    var merged = inc
                    merged.id = out[i].id
                    if out[i] != merged {
                        out[i] = merged
                        updated += 1
                    }
                } else {
                    out.append(inc)
                    added += 1
                }
            }
            return MergeResult(nodes: out, added: added, updated: updated, removed: 0)
        }
    }
}
