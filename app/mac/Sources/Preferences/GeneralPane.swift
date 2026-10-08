import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct GeneralPane: View {
    @Environment(ClientSettings.self) private var clientSettings
    @Environment(ServerSettings.self) private var serverSettings
    @Environment(NodeStore.self) private var nodeStore
    @Environment(Updater.self) private var updater
    @State private var launchAtLoginError: String?
    @State private var importStaged: SettingsExport?
    @State private var transferStatus: TransferStatus?

    private struct TransferStatus: Identifiable {
        let id = UUID()
        let ok: Bool
        let message: String
    }

    var body: some View {
        @Bindable var clientSettings = clientSettings
        @Bindable var serverSettings = serverSettings
        @Bindable var updater = updater
        Form {
            Section("Polling") {
                HStack {
                    Text("Local")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(serverSettings.localPollingIntervalSeconds) },
                            set: {
                                serverSettings.localPollingIntervalSeconds = Int($0.rounded())
                                serverSettings.persist()
                            }
                        ),
                        in: 1...60,
                        step: 1
                    )
                    Text("\(serverSettings.localPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
                HStack {
                    Text("SSH")
                        .frame(width: 60, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(serverSettings.sshPollingIntervalSeconds) },
                            set: {
                                serverSettings.sshPollingIntervalSeconds = Int($0.rounded())
                                serverSettings.persist()
                            }
                        ),
                        in: 1...300,
                        step: 1
                    )
                    Text("\(serverSettings.sshPollingIntervalSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
            }

            Section("Sleep & network") {
                HStack {
                    Text("Post-wake grace")
                        .frame(width: 120, alignment: .leading)
                    Slider(
                        value: Binding(
                            get: { Double(serverSettings.postWakeGraceSeconds) },
                            set: {
                                serverSettings.postWakeGraceSeconds = Int($0.rounded())
                                serverSettings.persist()
                            }
                        ),
                        in: 0...60,
                        step: 1
                    )
                    Text("\(serverSettings.postWakeGraceSeconds)s")
                        .font(.system(.body, design: .monospaced))
                        .frame(width: 56, alignment: .trailing)
                }
                Text("After Mac wake or the network returning, Towertail resumes polling but waits this long before firing any \"host is down\" notifications. Lets DHCP and Tailscale settle so a single slow reconnect doesn't page you.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Appearance") {
                Picker("Card density", selection: Binding(
                    get: { clientSettings.cardDensity },
                    set: { clientSettings.cardDensity = $0; clientSettings.persist() }
                )) {
                    Text("Dense").tag(CardDensity.a)
                    Text("Relaxed").tag(CardDensity.b)
                }
                .pickerStyle(.segmented)
            }

            Section("Terminal") {
                Picker("Default terminal", selection: Binding(
                    get: { clientSettings.defaultTerminalApp },
                    set: { clientSettings.defaultTerminalApp = $0; clientSettings.persist() }
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
                    get: { clientSettings.launchAtLogin },
                    set: { newValue in
                        let ok = LaunchAtLogin.shared.setEnabled(newValue)
                        if ok {
                            clientSettings.launchAtLogin = newValue
                            launchAtLoginError = nil
                        } else {
                            launchAtLoginError = LaunchAtLogin.shared.lastError
                        }
                        clientSettings.persist()
                    }
                ))
                if let err = launchAtLoginError {
                    Text(err)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }

            Section("Updates") {
                Toggle("Install updates automatically", isOn: $updater.autoInstall)
                HStack(spacing: 8) {
                    Button("Check for Updates") { updater.check(manual: true) }
                        .disabled(updater.busy)
                    Text(updater.statusText)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Text("v\(updater.current)")
                        .font(.system(.callout, design: .monospaced))
                        .foregroundStyle(.secondary)
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
        clientSettings.reloadFromDisk()
        serverSettings.reloadFromDisk()
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
