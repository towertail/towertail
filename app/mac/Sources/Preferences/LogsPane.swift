import SwiftUI
import AppKit

/// Live view over `Logger.shared.ring`. Lets the user filter by host and
/// minimum level so a noisy category doesn't drown out a rare error, and
/// opens the on-disk log directory in Finder for longer-range inspection.
///
/// The table reflects the in-memory ring only (≈2000 most recent events).
/// Anything older has already been rotated to the daily file on disk;
/// "Reveal in Finder" is the escape hatch for that.
struct LogsPane: View {
    private let logger = Logger.shared

    @State private var hostFilter: HostFilter = .all
    @State private var minLevel: Logger.Level = .debug
    @State private var search: String = ""
    @State private var sortOrder: [KeyPathComparator<LogRow>] = [
        .init(\.t, order: .reverse)
    ]
    @State private var selection: LogRow.ID?

    private enum HostFilter: Hashable {
        case all
        case app
        case host(String)

        var label: String {
            switch self {
            case .all: return "All hosts"
            case .app: return "App only"
            case .host(let name): return name
            }
        }
    }

    /// Precomputed row type so SwiftUI's `Table` + `KeyPathComparator`
    /// can sort on plain scalar keypaths. Mapping happens once per render
    /// so the projection stays cheap.
    private struct LogRow: Identifiable, Hashable {
        let id: UUID
        let t: Date
        let level: Logger.Level
        let levelRank: Int
        let category: String
        let host: String
        let message: String
        let detail: String
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            filterBar

            Table(rows, selection: $selection, sortOrder: $sortOrder) {
                TableColumn("Time", value: \.t) { row in
                    Text(Self.timeFormatter.string(from: row.t))
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(.secondary)
                }
                .width(min: 78, ideal: 86)

                TableColumn("Level", value: \.levelRank) { row in
                    levelBadge(row.level)
                }
                .width(min: 56, ideal: 60)

                TableColumn("Category", value: \.category) { row in
                    Text(row.category)
                        .font(.caption)
                }
                .width(min: 80, ideal: 110)

                TableColumn("Host", value: \.host) { row in
                    Text(row.host.isEmpty ? "—" : row.host)
                        .font(.caption)
                        .foregroundStyle(row.host.isEmpty ? .tertiary : .secondary)
                }
                .width(min: 80, ideal: 110)

                TableColumn("Message", value: \.message) { row in
                    VStack(alignment: .leading, spacing: 1) {
                        Text(row.message)
                        if !row.detail.isEmpty {
                            Text(row.detail)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .frame(minHeight: 220)

            footerBar
        }
    }

    // MARK: Filter / footer

    @ViewBuilder
    private var filterBar: some View {
        HStack(spacing: 8) {
            Picker("Host", selection: $hostFilter) {
                Text("All hosts").tag(HostFilter.all)
                Text("App only").tag(HostFilter.app)
                if !distinctHosts.isEmpty {
                    Divider()
                    ForEach(distinctHosts, id: \.self) { name in
                        Text(name).tag(HostFilter.host(name))
                    }
                }
            }
            .labelsHidden()
            .frame(maxWidth: 180)

            Picker("Level", selection: $minLevel) {
                ForEach(Logger.Level.allCases, id: \.self) { level in
                    Text(level.rawValue.trimmingCharacters(in: .whitespaces))
                        .tag(level)
                }
            }
            .labelsHidden()
            .frame(maxWidth: 100)

            TextField("Search", text: $search)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 220)

            Spacer()

            Text("\(rows.count) / \(logger.ring.count)")
                .font(.caption)
                .foregroundStyle(.secondary)
                .help("Rows shown / total in memory")
        }
    }

    @ViewBuilder
    private var footerBar: some View {
        HStack(spacing: 8) {
            Button {
                NSWorkspace.shared.activateFileViewerSelecting([logger.logsDirectory])
            } label: {
                Label("Reveal in Finder", systemImage: "folder")
            }
            .help("Open ~/Library/Application Support/Towertail/logs in Finder.")

            Button {
                logger.clearRing()
                selection = nil
            } label: {
                Label("Clear view", systemImage: "eraser")
            }
            .help("Clears the in-memory buffer only. On-disk log files are untouched.")

            Spacer()

            Text("On-disk log files kept for \(Logger.retentionDays) days.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    // MARK: Derived data

    private var distinctHosts: [String] {
        var seen = Set<String>()
        var ordered: [String] = []
        for e in logger.ring {
            if let name = e.hostName, !name.isEmpty, seen.insert(name).inserted {
                ordered.append(name)
            }
        }
        return ordered.sorted()
    }

    private var rows: [LogRow] {
        let needle = search.normalizedForSearch()
        let base = logger.ring.compactMap { entry -> LogRow? in
            if entry.level < minLevel { return nil }
            switch hostFilter {
            case .all:
                break
            case .app:
                if entry.hostName != nil { return nil }
            case .host(let name):
                if entry.hostName != name { return nil }
            }
            let host = entry.hostName ?? ""
            let detail = entry.kv
                .map { "\($0.0)=\($0.1)" }
                .joined(separator: " ")
            if !needle.isEmpty {
                let hay = "\(entry.category) \(host) \(entry.message) \(detail)".lowercased()
                if !hay.contains(needle) { return nil }
            }
            return LogRow(
                id: entry.id,
                t: entry.t,
                level: entry.level,
                levelRank: Self.rank(for: entry.level),
                category: entry.category,
                host: host,
                message: entry.message,
                detail: detail
            )
        }
        return base.sorted(using: sortOrder)
    }

    // MARK: Presentation helpers

    @ViewBuilder
    private func levelBadge(_ level: Logger.Level) -> some View {
        let (color, label): (Color, String) = {
            switch level {
            case .debug: return (.secondary, "DEBUG")
            case .info: return (.blue, "INFO")
            case .warn: return (.orange, "WARN")
            case .error: return (.red, "ERROR")
            }
        }()
        Text(label)
            .font(.system(.caption2, design: .monospaced).weight(.semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(color.opacity(0.12), in: Capsule())
    }

    private static func rank(for level: Logger.Level) -> Int {
        switch level {
        case .debug: return 0
        case .info: return 1
        case .warn: return 2
        case .error: return 3
        }
    }

    private static let timeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()
}
