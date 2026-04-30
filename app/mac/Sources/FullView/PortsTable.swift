import SwiftUI

/// Per-process ports table shown beneath the NET chart. Shape mirrors
/// `ProcessTable` but the data is a single snapshot (no time series): the
/// sampler refreshes ports every 10s by default and re-emits the cached
/// snapshot in between, so there is no useful per-tick history to replay.
/// `collectedAt` drives the staleness label in the header.
struct PortsTable: View {
    let ports: PortList?
    let available: Bool
    let isRootSampler: Bool
    /// Node the table is bound to. Drives the kill flow's transport
    /// (local /bin/kill vs. ssh kill). Optional: previews / older call
    /// sites can pass nil and the kill button hides.
    let node: Node?

    @State private var sort: [KeyPathComparator<PortRow>] = [
        KeyPathComparator(\PortRow.estOut, order: .reverse),
        KeyPathComparator(\PortRow.estIn, order: .reverse),
    ]

    /// Row queued for the kill confirmation. Mirrors `ProcessTable` —
    /// non-nil presents the dialog; cleared on cancel/confirm.
    @State private var killCandidate: PortRow?
    @State private var killResult: KillResult?
    @State private var killInFlight: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            header
            if !available {
                unavailable("Per-process ports collection disabled on this host.")
            } else if let ports {
                if ports.items.isEmpty {
                    unavailable("No processes with open sockets.")
                } else {
                    let rows = ports.items.map { PortRow(from: $0) }.sorted(using: sort)
                    Table(rows, sortOrder: $sort) {
                        TableColumn("PID", value: \.pid) { r in
                            PIDCell(
                                pid: r.pid,
                                name: r.name,
                                canKill: node != nil,
                                onKill: { killCandidate = r }
                            )
                        }
                        .width(min: 72, ideal: 88)

                        TableColumn("User", value: \.user) { r in
                            Text(r.user).lineLimit(1).textSelection(.enabled)
                        }
                        .width(min: 60, ideal: 90)

                        TableColumn("Name", value: \.name) { r in
                            Text(r.name).lineLimit(1).textSelection(.enabled)
                        }
                        .width(min: 100, ideal: 180)

                        TableColumn("Listen TCP", value: \.listenTCPSort) { r in
                            Text(r.listenTCP).lineLimit(1).textSelection(.enabled)
                                .help(r.listenTCP)
                        }
                        .width(min: 80, ideal: 140)

                        TableColumn("Listen UDP", value: \.listenUDPSort) { r in
                            Text(r.listenUDP).lineLimit(1).textSelection(.enabled)
                                .help(r.listenUDP)
                        }
                        .width(min: 80, ideal: 120)

                        TableColumn("Out", value: \.estOut) { r in
                            Text(verbatim: String(r.estOut)).monospacedDigit()
                                .textSelection(.enabled)
                        }
                        .width(min: 40, ideal: 50)

                        TableColumn("In", value: \.estIn) { r in
                            Text(verbatim: String(r.estIn)).monospacedDigit()
                                .textSelection(.enabled)
                        }
                        .width(min: 40, ideal: 50)

                        TableColumn("UDP", value: \.udpSockets) { r in
                            Text(verbatim: String(r.udpSockets)).monospacedDigit()
                                .textSelection(.enabled)
                        }
                        .width(min: 40, ideal: 50)

                        TableColumn("Top remotes", value: \.topRemotesSort) { r in
                            Text(r.topRemotes).lineLimit(1).textSelection(.enabled)
                                .help(r.topRemotes)
                        }
                        .width(min: 100, ideal: 180)
                    }
                    .frame(minHeight: 160, maxHeight: 360)
                }
            } else {
                unavailable("Waiting for first ports snapshot…")
            }
        }
        .padding(.horizontal, 16)
        .confirmationDialog(
            killDialogTitle,
            isPresented: Binding(
                get: { killCandidate != nil },
                set: { if !$0 { killCandidate = nil } }
            ),
            titleVisibility: .visible,
            presenting: killCandidate
        ) { row in
            Button(role: .destructive) {
                runKill(row: row)
            } label: {
                Text(verbatim: "Kill \(row.pid)")
            }
            .disabled(killInFlight || node == nil)
            Button("Cancel", role: .cancel) { }
        } message: { row in
            Text(killDialogMessage(for: row))
        }
        .alert(
            killAlertTitle,
            isPresented: Binding(
                get: { killResult != nil },
                set: { if !$0 { killResult = nil } }
            ),
            presenting: killResult
        ) { _ in
            Button("OK", role: .cancel) { killResult = nil }
        } message: { result in
            switch result {
            case .success(let pid, let name):
                Text(verbatim: "Sent SIGKILL to \(name) (PID \(pid)).")
            case .failure(let message):
                Text(message)
            }
        }
    }

    private var killDialogTitle: String {
        guard let row = killCandidate else { return "Kill process" }
        return "Kill \(row.name)?"
    }

    private var killAlertTitle: String {
        guard let r = killResult else { return "" }
        switch r {
        case .success: return "Process killed"
        case .failure: return "Kill failed"
        }
    }

    private func killDialogMessage(for row: PortRow) -> String {
        let cmd = killCommandPreview(pid: row.pid)
        return "This will run:\n\n\(cmd)\n\nThe process will be terminated immediately with SIGKILL."
    }

    private func killCommandPreview(pid: Int32) -> String {
        guard let node else { return "kill -9 \(pid)" }
        switch node.kind {
        case .local:
            return "kill -9 \(pid)"
        case .ssh:
            let user = node.sshUser ?? "?"
            let host = node.sshHost ?? "?"
            return "ssh \(user)@\(host) 'kill -9 \(pid)'"
        }
    }

    private func runKill(row: PortRow) {
        guard let node else { return }
        killInFlight = true
        Task {
            let outcome = await ProcessKiller.kill(pid: row.pid, node: node)
            await MainActor.run {
                killInFlight = false
                killCandidate = nil
                switch outcome {
                case .success:
                    killResult = .success(pid: row.pid, name: row.name)
                case .failure(let message):
                    killResult = .failure(message: message)
                }
            }
        }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Text("Ports").font(.headline)
            if isRootSampler {
                Label("root", systemImage: "shield.lefthalf.filled")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if available {
                Text("user scope")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help("Sampler running as a non-root user; on Linux this hides PIDs for other users' sockets.")
            }
            if let ports, ports.truncated {
                Text("truncated")
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .help("Connection cap (\(ports.maxConn)) was hit — some sockets are not represented.")
            }
            Spacer()
            if let ports {
                Text(ports.collectedTS.formatted(date: .omitted, time: .standard))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func unavailable(_ text: String) -> some View {
        VStack(spacing: 6) {
            Image(systemName: "network")
                .font(.title2).foregroundStyle(.secondary)
            Text(text).font(.callout).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 120)
    }
}

struct PortRow: Identifiable, Hashable {
    let id: Int32
    let pid: Int32
    let user: String
    let name: String
    let listenTCP: String
    let listenUDP: String
    let estOut: Int
    let estIn: Int
    let udpSockets: Int
    let topRemotes: String
    /// Sort proxies: smallest port if any, else .max so empty rows sort last.
    let listenTCPSort: UInt32
    let listenUDPSort: UInt32
    /// Sort proxy for "Top remotes": highest count's port. 0 when empty so
    /// empty rows sort to the bottom on a desc sort.
    let topRemotesSort: Int

    init(from p: PortItem) {
        self.id = p.pid
        self.pid = p.pid
        self.user = p.user ?? "—"
        self.name = p.name ?? "—"
        let tcp = p.listenTCP ?? []
        let udp = p.listenUDP ?? []
        self.listenTCP = tcp.isEmpty ? "—" : tcp.map { String($0) }.joined(separator: ", ")
        self.listenUDP = udp.isEmpty ? "—" : udp.map { String($0) }.joined(separator: ", ")
        self.listenTCPSort = tcp.first ?? .max
        self.listenUDPSort = udp.first ?? .max
        self.estOut = p.estOut
        self.estIn = p.estIn
        self.udpSockets = p.udpSockets ?? 0
        if let remotes = p.topRemotePorts, !remotes.isEmpty {
            self.topRemotes = remotes.map { ":\($0.port)×\($0.count)" }.joined(separator: " ")
            self.topRemotesSort = remotes.first?.count ?? 0
        } else {
            self.topRemotes = "—"
            self.topRemotesSort = 0
        }
    }
}
