import SwiftUI
import UniformTypeIdentifiers

/// The canonical columns a row can be mapped to. "Ignore" is explicit so
/// the user can drop noisy columns (comments, descriptions, etc.) without
/// having to edit the file.
enum CSVFieldRole: String, CaseIterable, Identifiable {
    case ignore
    case hostname
    case ip
    case sshUser
    case kind
    case tags

    var id: String { rawValue }

    var label: String {
        switch self {
        case .ignore: return "Ignore"
        case .hostname: return "Name / hostname"
        case .ip: return "IP / host"
        case .sshUser: return "SSH user"
        case .kind: return "Kind"
        case .tags: return "Tags"
        }
    }

    /// Heuristic auto-match from a header cell. Lowercased + trimmed on
    /// the caller side. Returns `ignore` for unknown headers rather than
    /// guessing — being wrong here is worse than being unopinionated.
    static func guess(from header: String) -> CSVFieldRole {
        let h = header.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        switch h {
        case "host", "hostname", "name", "display_name", "server":
            return .hostname
        case "ip", "address", "ansible_host", "ip_address":
            return .ip
        case "user", "ssh_user", "ansible_user", "login":
            return .sshUser
        case "kind", "type":
            return .kind
        case "tags", "labels", "groups":
            return .tags
        default:
            return .ignore
        }
    }
}

/// Step 2b — open a .csv/.tsv file and let the user confirm the column
/// mapping before the rows flow into the review grid.
struct CSVSource: View {
    @Binding var rows: [ImportRow]
    @Binding var error: String?

    @State private var rawRows: [[String]] = []
    @State private var hasHeader: Bool = true
    @State private var roles: [CSVFieldRole] = []
    @State private var filename: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("CSV / TSV")
                    .font(.headline)
                Spacer()
                Button("Choose File…") { pick() }
            }
            if let error {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                    Text(error).font(.callout)
                }
            }
            if !filename.isEmpty {
                Text(filename)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if !rawRows.isEmpty {
                Toggle("First row is a header", isOn: $hasHeader)
                    .onChange(of: hasHeader) { _, _ in rebuildMapping() }

                mappingHeader
                Divider()
                previewTable
            } else if error == nil {
                Spacer()
                Text("Pick a .csv or .tsv file to begin.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
            }
            Spacer()
        }
        .padding(20)
    }

    // MARK: - Mapping header

    @ViewBuilder
    private var mappingHeader: some View {
        let columns = rawRows.first?.count ?? 0
        if columns > 0 {
            ScrollView(.horizontal) {
                HStack(alignment: .top, spacing: 8) {
                    ForEach(0..<columns, id: \.self) { col in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(headerText(col: col))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .frame(width: 140, alignment: .leading)
                            Picker("", selection: Binding(
                                get: { roles.indices.contains(col) ? roles[col] : .ignore },
                                set: { newValue in
                                    if roles.indices.contains(col) {
                                        roles[col] = newValue
                                        applyMapping()
                                    }
                                }
                            )) {
                                ForEach(CSVFieldRole.allCases) { r in
                                    Text(r.label).tag(r)
                                }
                            }
                            .labelsHidden()
                            .frame(width: 140)
                        }
                    }
                }
            }
        }
    }

    private func headerText(col: Int) -> String {
        if hasHeader, let header = rawRows.first, header.indices.contains(col) {
            return header[col]
        }
        return "Column \(col + 1)"
    }

    // MARK: - Preview table

    private var previewTable: some View {
        let dataRows = hasHeader ? Array(rawRows.dropFirst()) : rawRows
        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(dataRows.prefix(20).enumerated()), id: \.offset) { _, r in
                    HStack(spacing: 10) {
                        ForEach(0..<r.count, id: \.self) { col in
                            Text(r[col])
                                .font(.system(.caption, design: .monospaced))
                                .frame(width: 140, alignment: .leading)
                                .lineLimit(1)
                        }
                    }
                    .padding(.vertical, 2)
                }
                if dataRows.count > 20 {
                    Text("… and \(dataRows.count - 20) more row\(dataRows.count - 20 == 1 ? "" : "s")")
                        .font(.caption).foregroundStyle(.secondary)
                        .padding(.top, 4)
                }
            }
        }
        .frame(maxHeight: 220)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(Color(nsColor: .textBackgroundColor))
        )
    }

    // MARK: - File pick

    private func pick() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.allowedContentTypes = [.commaSeparatedText, UTType(filenameExtension: "tsv") ?? .plainText, .plainText]
        if panel.runModal() == .OK, let url = panel.url {
            load(url: url)
        }
    }

    private func load(url: URL) {
        do {
            let text = try String(contentsOf: url, encoding: .utf8)
            let delim = CSVParser.delimiter(forExtension: url.pathExtension)
            let parsed = CSVParser.parse(text, delimiter: delim)
            filename = url.lastPathComponent
            rawRows = parsed
            error = nil
            rebuildMapping()
        } catch {
            self.error = "Couldn't read file: \(error.localizedDescription)"
            self.rawRows = []
            self.rows = []
        }
    }

    // MARK: - Mapping + row build

    private func rebuildMapping() {
        let columns = rawRows.first?.count ?? 0
        if hasHeader, let header = rawRows.first {
            roles = (0..<columns).map { col in
                header.indices.contains(col) ? CSVFieldRole.guess(from: header[col]) : .ignore
            }
        } else {
            // Without a header row there's nothing to guess from; fall back
            // to a conservative default the user can adjust with the
            // per-column dropdowns.
            roles = (0..<columns).map { col in
                switch col {
                case 0: return .hostname
                case 1: return .ip
                case 2: return .sshUser
                case 3: return .tags
                default: return .ignore
                }
            }
        }
        applyMapping()
    }

    private func applyMapping() {
        let dataRows = hasHeader ? Array(rawRows.dropFirst()) : rawRows
        let defaultUser = NSUserName()
        rows = dataRows.compactMap { cells -> ImportRow? in
            var hostname = ""
            var ip = ""
            var user = ""
            var kind: NodeKind = .ssh
            var tags: [String] = []
            for (col, val) in cells.enumerated() {
                guard roles.indices.contains(col) else { continue }
                let v = val.trimmingCharacters(in: .whitespacesAndNewlines)
                switch roles[col] {
                case .ignore: break
                case .hostname: hostname = v
                case .ip: ip = v
                case .sshUser: user = v
                case .kind: kind = (v.lowercased() == "local") ? .local : .ssh
                case .tags:
                    tags = v.split(whereSeparator: { $0 == "," || $0 == ";" || $0 == "|" })
                        .map { $0.trimmingCharacters(in: .whitespaces) }
                        .filter { !$0.isEmpty }
                }
            }
            let name = hostname.isEmpty ? ip : hostname
            let host = ip.isEmpty ? hostname : ip
            guard !name.isEmpty || !host.isEmpty else { return nil }
            return ImportRow(
                displayName: name,
                sshHost: host,
                sshUser: user.isEmpty ? defaultUser : user,
                kind: kind,
                tags: tags,
                included: true,
                status: .pending
            )
        }
    }
}
