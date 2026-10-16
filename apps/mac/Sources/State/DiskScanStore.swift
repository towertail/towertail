import Foundation
import Observation
import SQLite3

/// One stored scan result.
struct DiskScanRecord: Sendable, Equatable {
    var nodeID: UUID
    /// The requested root: "~" for the home folder, else the path.
    var scope: String
    var finishedAt: Date
    var durationMs: Int64
    var result: DiskScanResult
}

/// A change of the user to the cleanup list, kept per node and scope.
struct CleanupPick: Sendable, Equatable {
    enum Kind: String, Sendable {
        /// A folder or file that the user added. The key is the absolute path.
        case mine
        /// A hidden recommendation. The key is `<rule id>|<rec path>`.
        case hidden
    }

    var kind: Kind
    var key: String
    /// Mine: the item is in the script.
    var on: Bool = true
    var addedAt: Date

    static func hiddenKey(_ rec: DiskRec) -> String { rec.ruleID + "|" + rec.path }
}

/// Keeps the last scan results in the SQLite file of `HistoryStore`
/// (table `disk_scan`, gzip JSON). It keeps `keepPerScope` results for
/// each node and scope. It also keeps the cleanup picks of the user
/// (table `disk_cleanup_pick`).
@Observable
@MainActor
final class DiskScanStore {
    static let keepPerScope = 3

    /// Changes on each save, so that views reload.
    private(set) var revision = 0
    @ObservationIgnored private let db: DiskScanDB
    @ObservationIgnored private var cache: [String: [DiskScanRecord]] = [:]

    init(url: URL) {
        self.db = DiskScanDB(url: url)
    }

    func save(nodeID: UUID, scope: String, result: DiskScanResult, finishedAt: Date = Date()) {
        let record = DiskScanRecord(
            nodeID: nodeID, scope: scope, finishedAt: finishedAt,
            durationMs: result.durationMs, result: result
        )
        db.insert(record, keep: Self.keepPerScope)
        cache.removeAll()
        revision += 1
    }

    /// Puts `result` in place of the latest result of the scope. A refresh
    /// uses it, so that the merged result keeps the time of the full scan.
    func replaceLatest(nodeID: UUID, scope: String, result: DiskScanResult) {
        db.replaceLatest(nodeID: nodeID, scope: scope, result: result)
        cache.removeAll()
        revision += 1
    }

    /// Newest first.
    func records(nodeID: UUID, scope: String? = nil) -> [DiskScanRecord] {
        _ = revision
        let key = "\(nodeID.uuidString)|\(scope ?? "*")"
        if let hit = cache[key] { return hit }
        let rows = db.load(nodeID: nodeID, scope: scope)
        cache[key] = rows
        return rows
    }

    func latest(nodeID: UUID, scope: String? = nil) -> DiskScanRecord? {
        records(nodeID: nodeID, scope: scope).first
    }

    /// Oldest first.
    func picks(nodeID: UUID, scope: String) -> [CleanupPick] {
        db.picks(nodeID: nodeID, scope: scope)
    }

    func savePick(_ p: CleanupPick, nodeID: UUID, scope: String) {
        db.savePick(p, nodeID: nodeID, scope: scope)
    }

    func removePick(_ kind: CleanupPick.Kind, key: String, nodeID: UUID, scope: String) {
        db.removePick(kind, key: key, nodeID: nodeID, scope: scope)
    }

    func removeAll(nodeID: UUID) {
        db.delete(nodeID: nodeID)
        cache.removeAll()
        revision += 1
    }
}

/// SQLite access for `DiskScanStore`. A serial queue guards the handle.
final class DiskScanDB: @unchecked Sendable {
    private let queue = DispatchQueue(label: "com.towertail.diskscan", qos: .utility)
    private var db: OpaquePointer?
    private static let transient = unsafeBitCast(OpaquePointer(bitPattern: -1), to: sqlite3_destructor_type.self)

    init(url: URL) {
        queue.sync {
            try? FileManager.default.createDirectory(
                at: url.deletingLastPathComponent(), withIntermediateDirectories: true
            )
            guard sqlite3_open(url.path, &db) == SQLITE_OK else {
                db = nil
                return
            }
            sqlite3_busy_timeout(db, 2000)
            sqlite3_exec(db, """
            CREATE TABLE IF NOT EXISTS disk_scan (
                node_id TEXT NOT NULL,
                scope TEXT NOT NULL,
                finished_at REAL NOT NULL,
                duration_ms INTEGER NOT NULL,
                result_gz BLOB NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_disk_scan_node_scope ON disk_scan(node_id, scope, finished_at);
            CREATE TABLE IF NOT EXISTS disk_cleanup_pick (
                node_id TEXT NOT NULL,
                scope TEXT NOT NULL,
                kind TEXT NOT NULL,
                key TEXT NOT NULL,
                on_flag INTEGER NOT NULL,
                added_at REAL NOT NULL,
                PRIMARY KEY (node_id, scope, kind, key)
            );
            """, nil, nil, nil)
        }
    }

    deinit {
        queue.sync {
            if let db { sqlite3_close(db) }
        }
    }

    func insert(_ r: DiskScanRecord, keep: Int) {
        guard let json = try? JSONEncoder().encode(r.result) else { return }
        let blob = GzipEncoder.encode(json) ?? json
        queue.sync {
            guard let db else { return }
            var stmt: OpaquePointer?
            let sql = "INSERT INTO disk_scan (node_id, scope, finished_at, duration_ms, result_gz) VALUES (?, ?, ?, ?, ?);"
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, r.nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(stmt, 2, r.scope, -1, Self.transient)
            sqlite3_bind_double(stmt, 3, r.finishedAt.timeIntervalSince1970)
            sqlite3_bind_int64(stmt, 4, r.durationMs)
            _ = blob.withUnsafeBytes { raw in
                sqlite3_bind_blob(stmt, 5, raw.baseAddress, Int32(blob.count), Self.transient)
            }
            sqlite3_step(stmt)

            var prune: OpaquePointer?
            let pruneSQL = """
            DELETE FROM disk_scan WHERE node_id = ?1 AND scope = ?2 AND rowid NOT IN (
                SELECT rowid FROM disk_scan WHERE node_id = ?1 AND scope = ?2
                ORDER BY finished_at DESC LIMIT ?3
            );
            """
            guard sqlite3_prepare_v2(db, pruneSQL, -1, &prune, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(prune) }
            sqlite3_bind_text(prune, 1, r.nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(prune, 2, r.scope, -1, Self.transient)
            sqlite3_bind_int(prune, 3, Int32(keep))
            sqlite3_step(prune)
        }
    }

    func replaceLatest(nodeID: UUID, scope: String, result: DiskScanResult) {
        guard let json = try? JSONEncoder().encode(result) else { return }
        let blob = GzipEncoder.encode(json) ?? json
        queue.sync {
            guard let db else { return }
            var stmt: OpaquePointer?
            let sql = """
            UPDATE disk_scan SET result_gz = ?3 WHERE rowid = (
                SELECT rowid FROM disk_scan WHERE node_id = ?1 AND scope = ?2
                ORDER BY finished_at DESC LIMIT 1
            );
            """
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(stmt, 2, scope, -1, Self.transient)
            _ = blob.withUnsafeBytes { raw in
                sqlite3_bind_blob(stmt, 3, raw.baseAddress, Int32(blob.count), Self.transient)
            }
            sqlite3_step(stmt)
        }
    }

    func load(nodeID: UUID, scope: String?) -> [DiskScanRecord] {
        queue.sync {
            guard let db else { return [] }
            var stmt: OpaquePointer?
            let sql = scope == nil
                ? "SELECT scope, finished_at, duration_ms, result_gz FROM disk_scan WHERE node_id = ? ORDER BY finished_at DESC;"
                : "SELECT scope, finished_at, duration_ms, result_gz FROM disk_scan WHERE node_id = ? AND scope = ? ORDER BY finished_at DESC;"
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
            if let scope { sqlite3_bind_text(stmt, 2, scope, -1, Self.transient) }
            var out: [DiskScanRecord] = []
            while sqlite3_step(stmt) == SQLITE_ROW {
                let scope = String(cString: sqlite3_column_text(stmt, 0))
                let finished = Date(timeIntervalSince1970: sqlite3_column_double(stmt, 1))
                let duration = sqlite3_column_int64(stmt, 2)
                let len = Int(sqlite3_column_bytes(stmt, 3))
                guard let ptr = sqlite3_column_blob(stmt, 3), len > 0 else { continue }
                let blob = Data(bytes: ptr, count: len)
                let json = GzipDecoder.isGzip(blob) ? (GzipDecoder.decode(blob) ?? blob) : blob
                guard let result = try? JSONDecoder().decode(DiskScanResult.self, from: json) else { continue }
                out.append(DiskScanRecord(
                    nodeID: nodeID, scope: scope, finishedAt: finished, durationMs: duration, result: result
                ))
            }
            return out
        }
    }

    func delete(nodeID: UUID) {
        queue.sync {
            guard let db else { return }
            for table in ["disk_scan", "disk_cleanup_pick"] {
                var stmt: OpaquePointer?
                guard sqlite3_prepare_v2(db, "DELETE FROM \(table) WHERE node_id = ?;", -1, &stmt, nil) == SQLITE_OK else { continue }
                sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
                sqlite3_step(stmt)
                sqlite3_finalize(stmt)
            }
        }
    }

    func picks(nodeID: UUID, scope: String) -> [CleanupPick] {
        queue.sync {
            guard let db else { return [] }
            var stmt: OpaquePointer?
            let sql = "SELECT kind, key, on_flag, added_at FROM disk_cleanup_pick WHERE node_id = ? AND scope = ? ORDER BY added_at, rowid;"
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(stmt, 2, scope, -1, Self.transient)
            var out: [CleanupPick] = []
            while sqlite3_step(stmt) == SQLITE_ROW {
                guard let kind = CleanupPick.Kind(rawValue: String(cString: sqlite3_column_text(stmt, 0))) else { continue }
                out.append(CleanupPick(
                    kind: kind, key: String(cString: sqlite3_column_text(stmt, 1)),
                    on: sqlite3_column_int(stmt, 2) != 0,
                    addedAt: Date(timeIntervalSince1970: sqlite3_column_double(stmt, 3))
                ))
            }
            return out
        }
    }

    func savePick(_ p: CleanupPick, nodeID: UUID, scope: String) {
        queue.sync {
            guard let db else { return }
            var stmt: OpaquePointer?
            let sql = """
            INSERT INTO disk_cleanup_pick (node_id, scope, kind, key, on_flag, added_at) VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT (node_id, scope, kind, key) DO UPDATE SET on_flag = excluded.on_flag;
            """
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(stmt, 2, scope, -1, Self.transient)
            sqlite3_bind_text(stmt, 3, p.kind.rawValue, -1, Self.transient)
            sqlite3_bind_text(stmt, 4, p.key, -1, Self.transient)
            sqlite3_bind_int(stmt, 5, p.on ? 1 : 0)
            sqlite3_bind_double(stmt, 6, p.addedAt.timeIntervalSince1970)
            sqlite3_step(stmt)
        }
    }

    func removePick(_ kind: CleanupPick.Kind, key: String, nodeID: UUID, scope: String) {
        queue.sync {
            guard let db else { return }
            var stmt: OpaquePointer?
            let sql = "DELETE FROM disk_cleanup_pick WHERE node_id = ? AND scope = ? AND kind = ? AND key = ?;"
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, nodeID.uuidString, -1, Self.transient)
            sqlite3_bind_text(stmt, 2, scope, -1, Self.transient)
            sqlite3_bind_text(stmt, 3, kind.rawValue, -1, Self.transient)
            sqlite3_bind_text(stmt, 4, key, -1, Self.transient)
            sqlite3_step(stmt)
        }
    }
}
