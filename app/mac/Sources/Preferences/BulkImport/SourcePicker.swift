import SwiftUI

/// Step 1 of the bulk-import wizard: three large tiles the user clicks to
/// pick how they want to supply hosts. Tailscale dims with a hint when
/// it's not installed or its LocalAPI token can't be read.
struct SourcePicker: View {
    @Binding var selection: WizardSource?
    let onPick: (WizardSource) -> Void

    /// Checked at render time (not init) so a user who installs Tailscale
    /// while the wizard is open and reopens this step sees the tile enable.
    private var tailscaleAvailable: Bool { TailscaleLocalAPI.isAvailable() }

    var body: some View {
        VStack(spacing: 16) {
            Text("Import servers from…")
                .font(.title3)
                .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 14) {
                tile(
                    source: .tailscale,
                    icon: "network",
                    title: "Tailscale",
                    subtitle: tailscaleAvailable
                        ? "Auto-discover your tailnet peers."
                        : "Tailscale not detected",
                    enabled: tailscaleAvailable
                )
                tile(
                    source: .csv,
                    icon: "tablecells",
                    title: "CSV / TSV file",
                    subtitle: "Import a spreadsheet or Ansible inventory export.",
                    enabled: true
                )
                tile(
                    source: .paste,
                    icon: "text.justify.left",
                    title: "Paste list",
                    subtitle: "One host per line, or user@host.",
                    enabled: true
                )
            }
            Spacer()
        }
        .padding(20)
    }

    @ViewBuilder
    private func tile(
        source s: WizardSource,
        icon: String,
        title: String,
        subtitle: String,
        enabled: Bool
    ) -> some View {
        Button {
            guard enabled else { return }
            selection = s
            onPick(s)
        } label: {
            VStack(alignment: .leading, spacing: 8) {
                Image(systemName: icon)
                    .font(.system(size: 28, weight: .regular))
                    .foregroundStyle(enabled ? Color.accentColor : .secondary)
                Text(title)
                    .font(.headline)
                    .foregroundStyle(enabled ? .primary : .secondary)
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.leading)
                Spacer(minLength: 0)
            }
            .padding(14)
            .frame(width: 220, height: 140, alignment: .topLeading)
            .background(
                RoundedRectangle(cornerRadius: 10)
                    .fill(Color(nsColor: .controlBackgroundColor))
            )
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(Color.secondary.opacity(0.25), lineWidth: 1)
            )
            .opacity(enabled ? 1.0 : 0.55)
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
    }
}
