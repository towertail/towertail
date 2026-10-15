import Foundation
import Observation

/// Application-wide logger. Writes one line per event to a daily file at
/// `~/Library/Application Support/Towertail/logs/log.YYYY-MM-DD.log`,
/// also mirrors recent events into an in-memory ring buffer so the
/// Logs preferences pane can display them live without re-reading the
/// file.
///
/// Entry points are non-async and fire-and-forget: all disk I/O happens
/// on a dedicated serial DispatchQueue so caller threads (including
/// @MainActor views) never block. The trade-off is that an event emitted
/// moments before a crash may be lost — acceptable for a diagnostic
/// log, not audit-grade.
///
/// Format: `<ISO-8601> <level> <category> <host>? <message>  k=v k=v`
///
/// Example:
/// `2026-04-21T14:12:03.412Z INFO  auto-update host=vm auto-update: push starting from=0.0.0-dev+358e2da to=0.0.0-dev+715ce76`
@MainActor
@Observable
final class Logger {
    enum Level: String, Comparable, CaseIterable, Sendable {
        case debug = "DEBUG"
        case info = "INFO "
        case warn = "WARN "
        case error = "ERROR"

        static func < (lhs: Level, rhs: Level) -> Bool {
            let order: [Level] = [.debug, .info, .warn, .error]
            return order.firstIndex(of: lhs)! < order.firstIndex(of: rhs)!
        }
    }

    struct Entry: Identifiable, Sendable, Equatable {
        let id: UUID = UUID()
        let t: Date
        let level: Level
        let category: String
        let hostID: UUID?
        let hostName: String?
        let message: String
        let kv: [(String, String)]

        static func == (lhs: Entry, rhs: Entry) -> Bool { lhs.id == rhs.id }
    }

    static let shared = Logger()

    /// Retention: delete log files older than this many days at startup
    /// and whenever we rotate (new day). Nonisolated so the queue-private
    /// pruning helpers can read it without an actor hop.
    nonisolated static let retentionDays = 15

    /// How many recent entries to keep in memory for the Logs pane.
    /// Beyond this, older entries are dropped from the ring but remain
    /// on disk in the daily file.
    nonisolated static let ringCapacity = 2000

    private(set) var ring: [Entry] = []

    /// Dedicated serial queue so writes are ordered and off the main
    /// thread. Not @Observable-tracked.
    @ObservationIgnored
    private let writeQueue = DispatchQueue(
        label: "com.towertail.logger",
        qos: .utility
    )
    @ObservationIgnored
    private var currentFileURL: URL?
    @ObservationIgnored
    private var currentDayKey: String = ""
    @ObservationIgnored
    private lazy var iso: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
    @ObservationIgnored
    private lazy var dayKeyFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        f.timeZone = TimeZone.current
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    /// Logs directory. Public so the Logs preferences pane can "Reveal
    /// in Finder" without duplicating path logic.
    var logsDirectory: URL {
        let base = FileManager.default.urls(
            for: .applicationSupportDirectory, in: .userDomainMask
        ).first ?? URL(fileURLWithPath: NSHomeDirectory())
        return base
            .appendingPathComponent("Towertail", isDirectory: true)
            .appendingPathComponent("logs", isDirectory: true)
    }

    private init() {
        // One-shot housekeeping on first access.
        writeQueue.async { [weak self] in
            self?.ensureDirectory()
            self?.pruneOldLogsUnsafe()
        }
    }

    // MARK: Entry points

    func debug(_ message: String, category: String = "app", hostID: UUID? = nil, host: String? = nil, kv: [String: String] = [:]) {
        emit(.debug, message, category: category, hostID: hostID, host: host, kv: kv)
    }
    func info(_ message: String, category: String = "app", hostID: UUID? = nil, host: String? = nil, kv: [String: String] = [:]) {
        emit(.info, message, category: category, hostID: hostID, host: host, kv: kv)
    }
    func warn(_ message: String, category: String = "app", hostID: UUID? = nil, host: String? = nil, kv: [String: String] = [:]) {
        emit(.warn, message, category: category, hostID: hostID, host: host, kv: kv)
    }
    func error(_ message: String, category: String = "app", hostID: UUID? = nil, host: String? = nil, kv: [String: String] = [:]) {
        emit(.error, message, category: category, hostID: hostID, host: host, kv: kv)
    }

    /// Clears only the in-memory ring. Disk files are untouched.
    func clearRing() {
        ring = []
    }

    // MARK: Implementation

    private func emit(
        _ level: Level,
        _ message: String,
        category: String,
        hostID: UUID?,
        host: String?,
        kv: [String: String]
    ) {
        // Preserve insertion order by sorting kv keys alphabetically.
        // Dicts are unordered so logs wouldn't be diffable otherwise.
        let pairs: [(String, String)] = kv.sorted { $0.key < $1.key }.map { ($0.key, $0.value) }
        let entry = Entry(
            t: Date(),
            level: level,
            category: category,
            hostID: hostID,
            hostName: host,
            message: message,
            kv: pairs
        )
        // Ring first so the UI reflects the event immediately.
        ring.append(entry)
        if ring.count > Self.ringCapacity {
            ring.removeFirst(ring.count - Self.ringCapacity)
        }
        // Serialize the entry once on the main thread so our closure is
        // pure-value. Avoids holding @MainActor references across queue
        // hops.
        let line = formatLine(entry)
        let dayKey = dayKeyFormatter.string(from: entry.t)
        writeQueue.async { [weak self] in
            self?.writeLineUnsafe(line, dayKey: dayKey)
        }
    }

    private func formatLine(_ e: Entry) -> String {
        var out = "\(iso.string(from: e.t)) \(e.level.rawValue) \(e.category)"
        if let h = e.hostName {
            out += " host=\(h)"
        } else if let id = e.hostID {
            out += " host=\(id.uuidString.prefix(8))"
        }
        out += " "
        out += e.message
        for (k, v) in e.kv {
            out += " \(k)=\(escape(v))"
        }
        return out + "\n"
    }

    /// Minimally escape values so spaces / quotes don't break tailing
    /// the file with `cut`. We don't need strict shell quoting; just
    /// wrap values containing whitespace or `=` in double quotes.
    private func escape(_ s: String) -> String {
        if s.contains(" ") || s.contains("\"") || s.contains("\t") || s.isEmpty {
            let escaped = s.replacingOccurrences(of: "\"", with: "\\\"")
            return "\"\(escaped)\""
        }
        return s
    }

    // MARK: Queue-private (called only on writeQueue)

    nonisolated private func ensureDirectory() {
        let dir = Self.resolveLogsDir()
        try? FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: true
        )
    }

    nonisolated private static func resolveLogsDir() -> URL {
        let base = FileManager.default.urls(
            for: .applicationSupportDirectory, in: .userDomainMask
        ).first ?? URL(fileURLWithPath: NSHomeDirectory())
        return base
            .appendingPathComponent("Towertail", isDirectory: true)
            .appendingPathComponent("logs", isDirectory: true)
    }

    nonisolated private func writeLineUnsafe(_ line: String, dayKey: String) {
        // Resolve the target file, rotating to a new day if the key
        // changed since last call. Rotation also re-prunes so old files
        // age out even if the app stays running for weeks.
        let dir = Self.resolveLogsDir()
        let fileURL = dir.appendingPathComponent("log.\(dayKey).log")
        // Create if missing.
        if !FileManager.default.fileExists(atPath: fileURL.path) {
            try? "".write(to: fileURL, atomically: true, encoding: .utf8)
            pruneOldLogsUnsafe()
        }
        // Append.
        if let data = line.data(using: .utf8),
           let handle = try? FileHandle(forWritingTo: fileURL) {
            defer { try? handle.close() }
            do {
                try handle.seekToEnd()
                try handle.write(contentsOf: data)
            } catch {
                // Last-resort: write nothing. We can't log about
                // failing to log.
            }
        }
    }

    nonisolated private func pruneOldLogsUnsafe() {
        let dir = Self.resolveLogsDir()
        let cutoff = Date().addingTimeInterval(-Double(Self.retentionDays) * 86_400)
        guard let contents = try? FileManager.default.contentsOfDirectory(
            at: dir,
            includingPropertiesForKeys: [.contentModificationDateKey],
            options: [.skipsHiddenFiles]
        ) else { return }
        for url in contents where url.lastPathComponent.hasPrefix("log.") && url.pathExtension == "log" {
            if let attrs = try? url.resourceValues(forKeys: [.contentModificationDateKey]),
               let mdate = attrs.contentModificationDate,
               mdate < cutoff {
                try? FileManager.default.removeItem(at: url)
            }
        }
    }
}
