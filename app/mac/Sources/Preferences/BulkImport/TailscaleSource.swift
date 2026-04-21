import SwiftUI

/// Step 2a — auto-discovers tailnet peers via the Tailscale LocalAPI.
/// On success, populates the shared `rows` binding and is a no-op for
/// further renders (so flipping the MagicDNS toggle re-derives hosts from
/// the cached peer list without another HTTP round-trip).
struct TailscaleSource: View {
    @Binding var rows: [ImportRow]
    @Binding var error: String?
    @Binding var preferMagicDNS: Bool

    @State private var peers: [TailscalePeer] = []
    @State private var loading: Bool = false
    @State private var loaded: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Tailnet peers")
                    .font(.headline)
                Spacer()
                Toggle("Prefer MagicDNS name over IP", isOn: $preferMagicDNS)
                    .onChange(of: preferMagicDNS) { _, _ in rebuildRows() }
                    .help("When on, the host column uses your MagicDNS name (e.g. foo.tail8ed2.ts.net) instead of 100.x.x.x.")
            }

            if loading {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Fetching from Tailscale…")
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            } else if let error {
                VStack(alignment: .leading, spacing: 8) {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(.orange)
                        Text(error).font(.callout)
                    }
                    Button("Retry") { Task { await fetch() } }
                }
            } else if peers.isEmpty && loaded {
                Text("No peers found on your tailnet.")
                    .foregroundStyle(.secondary)
            } else {
                previewList
            }
            Spacer()
        }
        .padding(20)
        .task {
            if !loaded { await fetch() }
        }
    }

    private var previewList: some View {
        // Preview only — the authoritative, editable list lives in the
        // Review step. This view is just reassurance that we fetched the
        // right thing.
        VStack(alignment: .leading, spacing: 4) {
            Text("\(rows.count) server\(rows.count == 1 ? "" : "s") will be prepared. Continue to review and edit.")
                .font(.callout)
                .foregroundStyle(.secondary)
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(rows) { row in
                        HStack {
                            Text(row.displayName)
                                .font(.system(.body, design: .monospaced))
                            Spacer()
                            Text(row.sshHost)
                                .font(.system(.body, design: .monospaced))
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .frame(maxHeight: 280)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(Color(nsColor: .textBackgroundColor))
            )
        }
    }

    private func fetch() async {
        loading = true
        error = nil
        defer { loading = false; loaded = true }
        do {
            let fetched = try await TailscaleLocalAPI.fetchPeers()
                .filter { !$0.isSelf }
            await MainActor.run {
                self.peers = fetched
                rebuildRows()
            }
        } catch {
            await MainActor.run {
                self.error = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
                self.peers = []
                self.rows = []
            }
        }
    }

    private func rebuildRows() {
        let defaultUser = NSUserName()
        rows = peers.map { p in
            let host = preferMagicDNS ? p.displayMagicDNS : p.primaryIP
            let kind: NodeKind = p.os.lowercased() == "macos"
                ? .ssh // still SSH; no auto-local inference across tailnet
                : .ssh
            return ImportRow(
                displayName: p.hostname.isEmpty ? p.displayMagicDNS : p.hostname,
                sshHost: host,
                sshUser: defaultUser,
                kind: kind,
                tags: p.tags,
                included: true,
                status: .pending
            )
        }
    }
}
