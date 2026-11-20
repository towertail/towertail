import SwiftUI

struct WelcomeServersView: View {
    let flow: WelcomeFlow
    @State private var showAdd = false

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 16) {
                WelcomeHeading(
                    title: "Add your servers",
                    subtitle: "Pick the servers to watch. Towertail tests the SSH key on each one."
                )
                sources
                keyLine
                table
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 16)
            WelcomeFooter(
                step: .servers,
                secondary: .init(title: "Back", run: flow.back),
                primary: .init(
                    title: flow.adding ? "Adding…" : addTitle,
                    enabled: !flow.adding,
                    run: { Task { await add() } }
                )
            )
        }
        .task { await flow.loadSources() }
        .sheet(isPresented: $showAdd) {
            AddServerSheet { node in
                flow.addByHand(node)
                showAdd = false
            } onCancel: {
                showAdd = false
            }
        }
    }

    private var addTitle: String {
        let n = flow.selected.count
        return n == 0 ? "Continue" : "Add \(n) server\(n == 1 ? "" : "s")"
    }

    private func add() async {
        if !flow.selected.isEmpty { await flow.addSelected() }
        flow.next()
    }

    private var sources: some View {
        HStack(spacing: 8) {
            sourceButton("Tailscale", count: flow.tailscaleCount, source: .tailscale)
            sourceButton("~/.ssh/config", count: flow.sshConfigCount, source: .sshConfig)
            Button("By hand") { showAdd = true }
            Spacer()
        }
    }

    private func sourceButton(_ title: String, count: Int?, source: ServerCandidate.Source) -> some View {
        Button {
            flow.toggleAll(from: source)
        } label: {
            Text(count.map { "\(title) · \($0)" } ?? "\(title) · …")
        }
        .disabled((count ?? 0) == 0)
        .help(source == .tailscale ? (flow.tailscaleError ?? "Select all Tailscale servers") : "Select all servers of this source")
    }

    private var keyLine: some View {
        let total = flow.candidates.count
        return Text(total == 0
            ? "Key ~/.ssh/id_ed25519 is the key that Towertail uses."
            : "Key ~/.ssh/id_ed25519 works on \(flow.keyWorks) of \(total) servers.")
            .font(.system(size: 12))
            .foregroundStyle(.secondary)
    }

    private var table: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Color.clear.frame(width: 18, height: 1)
                Text("Server").frame(maxWidth: .infinity, alignment: .leading)
                Text("User").frame(width: 110, alignment: .leading)
                Text("OS").frame(width: 80, alignment: .leading)
                Text("SSH test").frame(width: 150, alignment: .leading)
            }
            .font(Typography.rowColumn)
            .foregroundStyle(.secondary)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            Divider()
            if flow.candidates.isEmpty {
                Text(flow.tailscaleCount == nil ? "Looking for servers…" : "No servers found. Use By hand to add one.")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVStack(spacing: 0) {
                        ForEach(flow.candidates) { row($0) }
                    }
                }
            }
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func row(_ c: ServerCandidate) -> some View {
        HStack(spacing: 10) {
            Toggle("", isOn: Binding(
                get: { flow.selected.contains(c.id) },
                set: { on in
                    if on { flow.selected.insert(c.id) } else { flow.selected.remove(c.id) }
                }
            ))
            .labelsHidden()
            .toggleStyle(.checkbox)
            .frame(width: 18)
            VStack(alignment: .leading, spacing: 1) {
                Text(c.name).font(Typography.rowName)
                Text(c.address).font(Typography.rowValue).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(c.user).font(Typography.rowValue).frame(width: 110, alignment: .leading)
            Text(c.os ?? "-").font(Typography.rowValue).frame(width: 80, alignment: .leading)
            TestBadge(result: c.test).frame(width: 150, alignment: .leading)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }
}

/// One line for the SSH test of a candidate.
private struct TestBadge: View {
    let result: ServerCandidate.TestResult

    var body: some View {
        switch result {
        case .untested:
            HStack(spacing: 6) {
                ProgressView().controlSize(.mini)
                Text("Testing").foregroundStyle(.secondary)
            }
            .font(.system(size: 11))
        case .ok(let ms):
            label("OK · \(ms) ms", tint: ThresholdTint.nominal.color)
        case .okNoSystemd:
            label("OK · no systemd", tint: ThresholdTint.warn.color)
        case .refused:
            label("Key refused", tint: ThresholdTint.critical.color)
        case .failed(let message):
            label(message, tint: ThresholdTint.critical.color)
        }
    }

    private func label(_ text: String, tint: Color) -> some View {
        HStack(spacing: 6) {
            Circle().fill(tint).frame(width: 7, height: 7)
            Text(text).lineLimit(1).truncationMode(.tail)
        }
        .font(.system(size: 11))
    }
}
