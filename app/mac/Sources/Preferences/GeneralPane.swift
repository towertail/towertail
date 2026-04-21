import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct GeneralPane: View {
    @Environment(AppSettings.self) private var settings
    @Environment(NodeStore.self) private var nodeStore
    @State private var launchAtLoginError: String?
    @State private var importStaged: SettingsExport?
    @State private var transferStatus: TransferStatus?

    private struct TransferStatus: Identifiable {
        let id = UUID()
        let ok: Bool
        let message: String
    }

    var body: some View {
        @Bindable var settings = settings
        Form {
            Section("Polling") {
                HStack {
                    Text("Local")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(settings.localPollingIntervalSeconds) },
                            set: {
                                settings.localPollingIntervalSeconds = Int($0.rounded())
                                settings.persist()
                            }
                        ),
                        in: 1...60,
                        step: 1
                    )
                    Text("\(settings.localPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
                HStack {
                    Text("SSH")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(settings.sshPollingIntervalSeconds) },
                            set: {
                                settings.sshPollingIntervalSeconds = Int($0.rounded())
                                settings.persist()
                            }
                        ),
                        in: 1...300,
                        step: 1
                    )
                    Text("\(settings.sshPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
            }

            Section("Appearance") {
                Picker("Card density", selection: Binding(
                    get: { settings.cardDensity },
                    set: { settings.cardDensity = $0; settings.persist() }
                )) {
                    Text("Dense").tag(CardDensity.a)
                    Text("Relaxed").tag(CardDensity.b)
                }
                .pickerStyle(.segmented)
            }

            Section("Terminal") {
                Picker("Default terminal", selection: Binding(
                    get: { settings.defaultTerminalApp },
                    set: { settings.defaultTerminalApp = $0; settings.persist() }
                )) {
                    ForEach(TerminalLauncher.supportedApps, id: \.self) { app in
                        Text(app).tag(app)
                    }
                }
                Text("Used when opening an SSH session from a server card.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Startup") {
                Toggle("Launch at login", isOn: Binding(
                    get: { settings.launchAtLogin },
                    set: { newValue in
                        let ok = LaunchAtLogin.shared.setEnabled(newValue)
                        if ok {
                            settings.launchAtLogin = newValue
                            launchAtLoginError = nil
                        } else {
                            launchAtLoginError = LaunchAtLogin.shared.lastError
                        }
                        settings.persist()
                    }
                ))
                if let err = launchAtLoginError {
                    Text(err)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }

            Section("Import & Export") {
                HStack(spacing: 8) {
                    Button("Export settings…") { exportSettings() }
                    Button("Import settings…") { importSettings() }
                    Spacer()
                }
                if let s = transferStatus {
                    HStack(spacing: 6) {
                        Image(systemName: s.ok ? "checkmark.circle.fill" : "xmark.octagon.fill")
                            .foregroundStyle(s.ok ? .green : .red)
                        Text(s.message)
                            .font(.callout)
                    }
                }
                Text("Export writes a JSON file with your servers, thresholds, notification preferences, and general settings. Import lets you pick which sections to bring in and whether to merge or overwrite the server list.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

        }
        .sheet(item: $importStaged) { export in
            ImportSettingsSheet(
                imported: export,
                onApply: { selection in
                    applyImport(export, selection: selection)
                    importStaged = nil
                },
                onCancel: { importStaged = nil }
            )
        }
    }

    // MARK: - Export

    private func exportSettings() {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.nameFieldStringValue = defaultExportName()
        panel.canCreateDirectories = true
        panel.title = "Export Towertail Settings"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            let p = SettingsPersistence.load(from: SettingsPersistence.defaultURL())
            let export = SettingsExport.from(
                p,
                appVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String
            )
            let data = try SettingsTransfer.encode(export)
            try data.write(to: url, options: .atomic)
            transferStatus = TransferStatus(ok: true, message: "Exported \(p.nodes.count) server\(p.nodes.count == 1 ? "" : "s") to \(url.lastPathComponent)")
            Logger.shared.info(
                "settings: exported",
                category: "settings",
                kv: ["nodes": String(p.nodes.count), "path": url.path]
            )
        } catch {
            transferStatus = TransferStatus(ok: false, message: "Export failed: \(error.localizedDescription)")
        }
    }

    private func defaultExportName() -> String {
        let df = DateFormatter()
        df.dateFormat = "yyyy-MM-dd"
        let host = Host.current().localizedName?.replacingOccurrences(of: " ", with: "-") ?? "mac"
        return "towertail-\(host)-\(df.string(from: Date())).json"
    }

    // MARK: - Import

    private func importSettings() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.title = "Import Towertail Settings"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            let data = try Data(contentsOf: url)
            let export = try SettingsTransfer.decode(data)
            importStaged = export
        } catch {
            transferStatus = TransferStatus(ok: false, message: error.localizedDescription)
        }
    }

    private func applyImport(_ export: SettingsExport, selection: ImportSelection) {
        let url = SettingsPersistence.defaultURL()
        let base = SettingsPersistence.load(from: url)
        let (merged, report) = SettingsTransfer.apply(export, to: base, selection: selection)
        _ = SettingsPersistence.save(merged, to: url)
        settings.reloadFromDisk()
        nodeStore.replaceAllFromDisk()
        transferStatus = TransferStatus(ok: true, message: "Imported: \(report.summary)")
        Logger.shared.info(
            "settings: imported",
            category: "settings",
            kv: [
                "general": String(report.generalApplied),
                "thresholds": String(report.globalThresholdsApplied),
                "notifications": String(report.notificationsApplied),
                "serversAdded": String(report.serversAdded),
                "serversUpdated": String(report.serversUpdated),
                "serversRemoved": String(report.serversRemoved),
                "serverThresholdsUpdated": String(report.serverThresholdsUpdated)
            ]
        )
    }
}

extension SettingsExport: Identifiable {
    var id: Date { exportedAt }
}
