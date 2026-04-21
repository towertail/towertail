import SwiftUI

/// Step 2c — let the user paste a list of hosts, one per line. Lines
/// starting with `#` or blank are ignored. `user@host` syntax is honored
/// on a per-line basis; everything else inherits the default SSH user.
struct PasteSource: View {
    @Binding var rows: [ImportRow]

    @State private var text: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Paste hosts")
                .font(.headline)
            Text("One per line. `user@host` is honored. Lines starting with `#` or blank are ignored.")
                .font(.caption)
                .foregroundStyle(.secondary)

            TextEditor(text: $text)
                .font(.system(.body, design: .monospaced))
                .frame(minHeight: 200)
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(Color.secondary.opacity(0.3), lineWidth: 1)
                )
                .onChange(of: text) { _, _ in rebuild() }

            Text("\(rows.count) host\(rows.count == 1 ? "" : "s") detected")
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(20)
    }

    private func rebuild() {
        let defaultUser = NSUserName()
        var out: [ImportRow] = []
        for raw in text.split(whereSeparator: { $0.isNewline }) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.isEmpty { continue }
            if line.hasPrefix("#") { continue }

            let user: String
            let host: String
            if let at = line.firstIndex(of: "@") {
                user = String(line[..<at]).trimmingCharacters(in: .whitespaces)
                host = String(line[line.index(after: at)...]).trimmingCharacters(in: .whitespaces)
            } else {
                user = defaultUser
                host = String(line)
            }
            guard !host.isEmpty else { continue }
            out.append(ImportRow(
                displayName: host,
                sshHost: host,
                sshUser: user,
                kind: .ssh,
                tags: [],
                included: true,
                status: .pending
            ))
        }
        rows = out
    }
}
