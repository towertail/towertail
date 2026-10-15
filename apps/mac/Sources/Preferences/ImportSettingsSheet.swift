import SwiftUI

/// Presented after the user has picked a valid Towertail settings export
/// file. Lets them pick which sections to bring over and — for servers —
/// whether to merge into or overwrite the existing list.
struct ImportSettingsSheet: View {
    let imported: SettingsExport
    let onApply: (ImportSelection) -> Void
    let onCancel: () -> Void

    @State private var selection: ImportSelection = .allDefaults

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
                .padding(.horizontal)
                .padding(.top)

            Divider().padding(.vertical, 8)

            Form {
                Section("What to import") {
                    Toggle("General (polling, density, terminal, startup)", isOn: $selection.general)
                    Toggle("Global thresholds (CPU / memory / disk)", isOn: $selection.globalThresholds)
                    Toggle("Notification preferences", isOn: $selection.notifications)
                    Toggle(
                        "Servers (\(imported.nodes.count) host\(imported.nodes.count == 1 ? "" : "s"))",
                        isOn: $selection.servers
                    )
                    Toggle("Per-server threshold overrides", isOn: $selection.serverThresholds)
                        .disabled(selection.servers)
                        .help(
                            selection.servers
                                ? "Included with Servers."
                                : "Apply custom CPU/memory/disk overrides onto existing servers without adding or replacing hosts."
                        )
                }

                if selection.servers {
                    Section("Servers strategy") {
                        Picker("", selection: $selection.serverStrategy) {
                            Text("Merge").tag(ImportSelection.ServerStrategy.merge)
                            Text("Overwrite").tag(ImportSelection.ServerStrategy.overwrite)
                        }
                        .pickerStyle(.segmented)
                        .labelsHidden()

                        Text(strategyExplanation)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .formStyle(.grouped)

            Divider()

            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { onCancel() }
                Button("Import") { onApply(selection) }
                    .keyboardShortcut(.defaultAction)
                    .disabled(nothingSelected)
            }
            .padding()
        }
        .frame(minWidth: 480, minHeight: 420)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Import Settings")
                .font(.title3).bold()
            HStack(spacing: 6) {
                Text("Exported")
                Text(imported.exportedAt.formatted(date: .abbreviated, time: .shortened))
                if let v = imported.appVersion {
                    Text("· Towertail \(v)")
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var nothingSelected: Bool {
        !(selection.general
          || selection.globalThresholds
          || selection.notifications
          || selection.servers
          || selection.serverThresholds)
    }

    private var strategyExplanation: String {
        switch selection.serverStrategy {
        case .merge:
            return "Add imported hosts that don't already exist; update matching hosts in place. Existing hosts not in the file are kept."
        case .overwrite:
            return "Replace the entire server list with the imported one. Hosts not in the file will be removed."
        }
    }
}
