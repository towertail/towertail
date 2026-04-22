import Foundation
import SQLite3

/// Minimal SQLite-backed ring of recent samples so the popover can show
/// history after an app restart. Writes are append-only and happen off the
/// main actor. A single background serial queue guards the sqlite handle.
final class HistoryStore: @unchecked Sendable {
    static let maxRowsPerNode = 2_000
    /// Per-process snapshots are ~2-10 KB each. 2h × 2s poll = 3600 rows,
    /// ~20 MB per host worst case. We keep the same 2h window as the
    /// in-memory `ProcSeries` so restart restores the full scrubbable
    /// history without blowing up disk. Rows older than this are pruned
    /// on each append.
    static let procRetention: TimeInterval = 2 * 60 * 60
    /// Same 2h window for per-mount capacity and per-device disk I/O —
    /// so a restart restores the full DISK-tab history, not just whatever
    /// has been ingested since launch.
    static let diskRetention: TimeInterval = 2 * 60 * 60

    private let url: URL
    private let queue = DispatchQueue(label: "com.towertail.history", qos: .utility)
    private var db: OpaquePointer?
    private var insertStmt: OpaquePointer?
    private var insertProcsStmt: OpaquePointer?
    private var pruneProcsStmt: OpaquePointer?
    private var insertDiskCapStmt: OpaquePointer?
    private var pruneDiskCapStmt: OpaquePointer?
    private var insertDiskIOStmt: OpaquePointer?
    private var pruneDiskIOStmt: OpaquePointer?
    /// Lazily-initialized JSON codec for proc item lists. Decoder uses
    /// fractional-seconds ISO-8601 so round-trip with sampler-produced
    /// start_ts works; encoder mirrors that format.
    private static let procDecoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let s = try c.decode(String.self)
            let fmt = ISO8601DateFormatter()
            fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let parsed = fmt.date(from: s) { return parsed }
            fmt.formatOptions = [.withInternetDateTime]
            if let parsed = fmt.date(from: s) { return parsed }
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "invalid ts: \(s)")
        }
        return d
    }()
    private static let procEncoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .custom { date, encoder in
            let fmt = ISO8601DateFormatter()
            fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            var c = encoder.singleValueContainer()
            try c.encode(fmt.string(from: date))
        }
        return e
    }()

    init(url: URL) {
        self.url = url
        queue.sync {
            openAndMigrate()
        }
    }

    deinit {
        queue.sync {
            if let insertStmt { sqlite3_finalize(insertStmt) }
            if let insertProcsStmt { sqlite3_finalize(insertProcsStmt) }
            if let pruneProcsStmt { sqlite3_finalize(pruneProcsStmt) }
            if let insertDiskCapStmt { sqlite3_finalize(insertDiskCapStmt) }
            if let pruneDiskCapStmt { sqlite3_finalize(pruneDiskCapStmt) }
            if let insertDiskIOStmt { sqlite3_finalize(insertDiskIOStmt) }
            if let pruneDiskIOStmt { sqlite3_finalize(pruneDiskIOStmt) }
            if let db { sqlite3_close(db) }
        }
    }

    private func openAndMigrate() {
        let dir = url.deletingLastPathComponent()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        if sqlite3_open(url.path, &db) != SQLITE_OK {
            db = nil
            return
        }
        let sql = """
        CREATE TABLE IF NOT EXISTS samples (
            node_id TEXT NOT NULL,
            ts REAL NOT NULL,
            cpu REAL,
            mem REAL,
            disk REAL,
            net REAL,
            rx_mbps REAL,
            tx_mbps REAL
        );
        CREATE INDEX IF NOT EXISTS idx_samples_node_ts ON samples(node_id, ts);
        CREATE TABLE IF NOT EXISTS proc_snapshots (
            node_id TEXT NOT NULL,
            ts REAL NOT NULL,
            root INTEGER NOT NULL,
            items_json BLOB NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_proc_snapshots_node_ts ON proc_snapshots(node_id, ts);
        CREATE TABLE IF NOT EXISTS disk_capacity (
            node_id TEXT NOT NULL,
            ts REAL NOT NULL,
            mount TEXT NOT NULL,
            frac REAL NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_disk_capacity_node_ts ON disk_capacity(node_id, ts);
        CREATE TABLE IF NOT EXISTS disk_io (
            node_id TEXT NOT NULL,
            ts REAL NOT NULL,
            device TEXT NOT NULL,
            read_mbps REAL NOT NULL,
            write_mbps REAL NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_disk_io_node_ts ON disk_io(node_id, ts);
        """
        sqlite3_exec(db, sql, nil, nil, nil)

        let insertSQL = """
        INSERT INTO samples (node_id, ts, cpu, mem, disk, net, rx_mbps, tx_mbps)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?);
        """
        sqlite3_prepare_v2(db, insertSQL, -1, &insertStmt, nil)

        let insertProcsSQL = """
        INSERT INTO proc_snapshots (node_id, ts, root, items_json)
        VALUES (?, ?, ?, ?);
        """
        sqlite3_prepare_v2(db, insertProcsSQL, -1, &insertProcsStmt, nil)

        // Cached so the per-append retention prune avoids recompiling the
        // statement every tick — with ~8 hosts polling every 2s that was
        // 4 prepare/finalize cycles per second of otherwise-idle work.
        let pruneProcsSQL = "DELETE FROM proc_snapshots WHERE node_id = ? AND ts < ?;"
        sqlite3_prepare_v2(db, pruneProcsSQL, -1, &pruneProcsStmt, nil)

        let insertDiskCapSQL = """
        INSERT INTO disk_capacity (node_id, ts, mount, frac)
        VALUES (?, ?, ?, ?);
        """
        sqlite3_prepare_v2(db, insertDiskCapSQL, -1, &insertDiskCapStmt, nil)

        let pruneDiskCapSQL = "DELETE FROM disk_capacity WHERE node_id = ? AND ts < ?;"
        sqlite3_prepare_v2(db, pruneDiskCapSQL, -1, &pruneDiskCapStmt, nil)

        let insertDiskIOSQL = """
        INSERT INTO disk_io (node_id, ts, device, read_mbps, write_mbps)
        VALUES (?, ?, ?, ?, ?);
        """
        sqlite3_prepare_v2(db, insertDiskIOSQL, -1, &insertDiskIOStmt, nil)

        let pruneDiskIOSQL = "DELETE FROM disk_io WHERE node_id = ? AND ts < ?;"
        sqlite3_prepare_v2(db, pruneDiskIOSQL, -1, &pruneDiskIOStmt, nil)
    }

    /// Append a point for a node. Called from any context; non-blocking.
    func append(nodeID: UUID, point: HistoryPoint) {
        let id = nodeID.uuidString
        queue.async { [weak self] in
            guard let self, let stmt = self.insertStmt else { return }
            sqlite3_reset(stmt)
            sqlite3_clear_bindings(stmt)
            _ = id.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_double(stmt, 2, point.t.timeIntervalSince1970)
            Self.bindOptional(stmt, 3, point.cpu)
            Self.bindOptional(stmt, 4, point.mem)
            Self.bindOptional(stmt, 5, point.disk)
            Self.bindOptional(stmt, 6, point.net)
            Self.bindOptional(stmt, 7, point.rxMBps)
            Self.bindOptional(stmt, 8, point.txMBps)
            sqlite3_step(stmt)
        }
    }

    /// Load the most recent rows for a node, oldest first.
    func loadRecent(nodeID: UUID, limit: Int = HistoryStore.maxRowsPerNode) -> [HistoryPoint] {
        queue.sync {
            guard let db else { return [] }
            let sql = """
            SELECT ts, cpu, mem, disk, net, rx_mbps, tx_mbps
            FROM samples WHERE node_id = ?
            ORDER BY ts DESC LIMIT ?;
            """
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            _ = nodeID.uuidString.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_int(stmt, 2, Int32(limit))
            var out: [HistoryPoint] = []
            out.reserveCapacity(min(limit, 512))
            while sqlite3_step(stmt) == SQLITE_ROW {
                let ts = sqlite3_column_double(stmt, 0)
                let point = HistoryPoint(
                    t: Date(timeIntervalSince1970: ts),
                    cpu: Self.readOptional(stmt, 1),
                    mem: Self.readOptional(stmt, 2),
                    disk: Self.readOptional(stmt, 3),
                    net: Self.readOptional(stmt, 4),
                    rxMBps: Self.readOptional(stmt, 5),
                    txMBps: Self.readOptional(stmt, 6)
                )
                out.append(point)
            }
            return out.reversed()
        }
    }

    /// Trim rows older than the most recent `keep` per node.
    func trim(nodeID: UUID, keep: Int = HistoryStore.maxRowsPerNode) {
        let id = nodeID.uuidString
        queue.async { [weak self] in
            guard let self, let db = self.db else { return }
            let sql = """
            DELETE FROM samples
            WHERE node_id = ?
              AND ts < (
                SELECT MIN(ts) FROM (
                  SELECT ts FROM samples WHERE node_id = ?
                  ORDER BY ts DESC LIMIT ?
                )
              );
            """
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return }
            defer { sqlite3_finalize(stmt) }
            _ = id.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            _ = id.withCString { cstr in
                sqlite3_bind_text(stmt, 2, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_int(stmt, 3, Int32(keep))
            sqlite3_step(stmt)
        }
    }

    /// Persist a process snapshot. Serializes items as JSON so the full
    /// sampler payload (cmd line, counters, threads, etc.) round-trips
    /// without schema churn when we add fields to `ProcSample`.
    ///
    /// Also opportunistically prunes rows older than `procRetention`
    /// relative to this write — cheap to do here (indexed range delete)
    /// and keeps disk usage bounded without a separate timer.
    func appendProcs(nodeID: UUID, t: Date, root: Bool, items: [ProcSample]) {
        let id = nodeID.uuidString
        let ts = t.timeIntervalSince1970
        // Encode on the caller's thread so we don't capture `[ProcSample]`
        // (not Sendable) into a @Sendable DispatchQueue closure. Failures
        // here mean we drop this one snapshot silently — better than
        // crashing the collector.
        guard let payload = try? Self.procEncoder.encode(items) else { return }
        queue.async { [weak self] in
            guard let self, let stmt = self.insertProcsStmt else { return }
            sqlite3_reset(stmt)
            sqlite3_clear_bindings(stmt)
            _ = id.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_double(stmt, 2, ts)
            sqlite3_bind_int(stmt, 3, root ? 1 : 0)
            _ = payload.withUnsafeBytes { raw -> Int32 in
                sqlite3_bind_blob(stmt, 4, raw.baseAddress, Int32(payload.count), Self.sqliteTransient)
            }
            sqlite3_step(stmt)

            // Prune older than retention — indexed range delete is ~O(log N + K).
            if let prune = self.pruneProcsStmt {
                let cutoff = ts - Self.procRetention
                sqlite3_reset(prune)
                sqlite3_clear_bindings(prune)
                _ = id.withCString { cstr in
                    sqlite3_bind_text(prune, 1, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(prune, 2, cutoff)
                sqlite3_step(prune)
            }
        }
    }

    struct ProcHistoryRow: Sendable {
        let t: Date
        let root: Bool
        let items: [ProcSample]
    }

    /// Load persisted process snapshots for a node, oldest first. Only
    /// rows newer than `procRetention` are returned — we don't want to
    /// hydrate an hour of empty space before whatever's fresh.
    func loadRecentProcs(nodeID: UUID) -> [ProcHistoryRow] {
        queue.sync {
            guard let db else { return [] }
            let cutoff = Date().timeIntervalSince1970 - Self.procRetention
            let sql = """
            SELECT ts, root, items_json
            FROM proc_snapshots
            WHERE node_id = ? AND ts >= ?
            ORDER BY ts ASC;
            """
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            _ = nodeID.uuidString.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_double(stmt, 2, cutoff)
            var out: [ProcHistoryRow] = []
            while sqlite3_step(stmt) == SQLITE_ROW {
                let ts = sqlite3_column_double(stmt, 0)
                let root = sqlite3_column_int(stmt, 1) != 0
                guard let bytes = sqlite3_column_blob(stmt, 2) else { continue }
                let n = Int(sqlite3_column_bytes(stmt, 2))
                let data = Data(bytes: bytes, count: n)
                let items = (try? Self.procDecoder.decode([ProcSample].self, from: data)) ?? []
                out.append(ProcHistoryRow(
                    t: Date(timeIntervalSince1970: ts),
                    root: root,
                    items: items
                ))
            }
            return out
        }
    }

    /// Append one per-mount capacity snapshot. `mounts` is the raw
    /// `[DiskSample]` from the sampler — we convert to a used-fraction
    /// here so the hydrate path can replay directly into DiskSeries
    /// without needing a second total/used column.
    ///
    /// Prunes rows older than `diskRetention` on each append, same
    /// pattern as procs — single indexed range delete per ingest.
    func appendDiskCapacity(nodeID: UUID, t: Date, mounts: [DiskSample]) {
        guard !mounts.isEmpty else { return }
        let id = nodeID.uuidString
        let ts = t.timeIntervalSince1970
        // Snapshot into Sendable-safe pairs so the closure doesn't
        // capture `[DiskSample]` (Decodable-only, not explicitly Sendable
        // via its Int64 fields — still fine, but keep the closure minimal).
        let rows: [(mount: String, frac: Double)] = mounts.map { d in
            let f = d.total > 0 ? Double(d.used) / Double(d.total) : 0
            return (d.mount, Swift.min(Swift.max(f, 0), 1))
        }
        queue.async { [weak self] in
            guard let self, let stmt = self.insertDiskCapStmt else { return }
            for row in rows {
                sqlite3_reset(stmt)
                sqlite3_clear_bindings(stmt)
                _ = id.withCString { cstr in
                    sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(stmt, 2, ts)
                _ = row.mount.withCString { cstr in
                    sqlite3_bind_text(stmt, 3, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(stmt, 4, row.frac)
                sqlite3_step(stmt)
            }
            if let prune = self.pruneDiskCapStmt {
                let cutoff = ts - Self.diskRetention
                sqlite3_reset(prune)
                sqlite3_clear_bindings(prune)
                _ = id.withCString { cstr in
                    sqlite3_bind_text(prune, 1, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(prune, 2, cutoff)
                sqlite3_step(prune)
            }
        }
    }

    /// Append one per-device disk I/O snapshot. The scalar total is
    /// persisted as a row with `device = ""`; named devices keep their
    /// sampler-reported names. Values are stored in MB/s to match the
    /// in-memory DiskIOSeries representation — no unit conversion on
    /// hydrate.
    func appendDiskIO(
        nodeID: UUID, t: Date,
        totalReadBps: Int64, totalWriteBps: Int64,
        devices: [DiskIODevice]?
    ) {
        let id = nodeID.uuidString
        let ts = t.timeIntervalSince1970
        var rows: [(device: String, read: Double, write: Double)] = [(
            "",
            Self.bytesToMBps(totalReadBps),
            Self.bytesToMBps(totalWriteBps)
        )]
        if let devices {
            for d in devices {
                rows.append((d.name, Self.bytesToMBps(d.readBps), Self.bytesToMBps(d.writeBps)))
            }
        }
        queue.async { [weak self] in
            guard let self, let stmt = self.insertDiskIOStmt else { return }
            for row in rows {
                sqlite3_reset(stmt)
                sqlite3_clear_bindings(stmt)
                _ = id.withCString { cstr in
                    sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(stmt, 2, ts)
                _ = row.device.withCString { cstr in
                    sqlite3_bind_text(stmt, 3, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(stmt, 4, row.read)
                sqlite3_bind_double(stmt, 5, row.write)
                sqlite3_step(stmt)
            }
            if let prune = self.pruneDiskIOStmt {
                let cutoff = ts - Self.diskRetention
                sqlite3_reset(prune)
                sqlite3_clear_bindings(prune)
                _ = id.withCString { cstr in
                    sqlite3_bind_text(prune, 1, cstr, -1, Self.sqliteTransient)
                }
                sqlite3_bind_double(prune, 2, cutoff)
                sqlite3_step(prune)
            }
        }
    }

    struct DiskCapacityRow: Sendable {
        let t: Date
        let mount: String
        let frac: Double
    }

    struct DiskIORow: Sendable {
        let t: Date
        /// Empty string means the cross-device scalar total.
        let device: String
        let readMBps: Double
        let writeMBps: Double
    }

    /// Load persisted per-mount capacity rows for a node, oldest first.
    /// Only rows newer than `diskRetention` are returned so a restart
    /// doesn't backfill an empty gap with stale data.
    func loadRecentDiskCapacity(nodeID: UUID) -> [DiskCapacityRow] {
        queue.sync {
            guard let db else { return [] }
            let cutoff = Date().timeIntervalSince1970 - Self.diskRetention
            let sql = """
            SELECT ts, mount, frac
            FROM disk_capacity
            WHERE node_id = ? AND ts >= ?
            ORDER BY ts ASC;
            """
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            _ = nodeID.uuidString.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_double(stmt, 2, cutoff)
            var out: [DiskCapacityRow] = []
            while sqlite3_step(stmt) == SQLITE_ROW {
                let ts = sqlite3_column_double(stmt, 0)
                guard let cstr = sqlite3_column_text(stmt, 1) else { continue }
                let mount = String(cString: cstr)
                let frac = sqlite3_column_double(stmt, 2)
                out.append(DiskCapacityRow(
                    t: Date(timeIntervalSince1970: ts),
                    mount: mount, frac: frac
                ))
            }
            return out
        }
    }

    /// Load persisted per-device I/O rows for a node, oldest first.
    /// Empty-string `device` rows are the scalar total. Rows older than
    /// `diskRetention` are excluded.
    func loadRecentDiskIO(nodeID: UUID) -> [DiskIORow] {
        queue.sync {
            guard let db else { return [] }
            let cutoff = Date().timeIntervalSince1970 - Self.diskRetention
            let sql = """
            SELECT ts, device, read_mbps, write_mbps
            FROM disk_io
            WHERE node_id = ? AND ts >= ?
            ORDER BY ts ASC;
            """
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
            defer { sqlite3_finalize(stmt) }
            _ = nodeID.uuidString.withCString { cstr in
                sqlite3_bind_text(stmt, 1, cstr, -1, Self.sqliteTransient)
            }
            sqlite3_bind_double(stmt, 2, cutoff)
            var out: [DiskIORow] = []
            while sqlite3_step(stmt) == SQLITE_ROW {
                let ts = sqlite3_column_double(stmt, 0)
                guard let cstr = sqlite3_column_text(stmt, 1) else { continue }
                let device = String(cString: cstr)
                let r = sqlite3_column_double(stmt, 2)
                let w = sqlite3_column_double(stmt, 3)
                out.append(DiskIORow(
                    t: Date(timeIntervalSince1970: ts),
                    device: device, readMBps: r, writeMBps: w
                ))
            }
            return out
        }
    }

    static func defaultURL() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("Towertail", isDirectory: true)
            .appendingPathComponent("history.sqlite")
    }

    private static let sqliteTransient = unsafeBitCast(
        OpaquePointer(bitPattern: -1), to: sqlite3_destructor_type.self
    )

    private static func bindOptional(_ stmt: OpaquePointer, _ idx: Int32, _ v: Double?) {
        if let v { sqlite3_bind_double(stmt, idx, v) } else { sqlite3_bind_null(stmt, idx) }
    }

    private static func readOptional(_ stmt: OpaquePointer?, _ idx: Int32) -> Double? {
        guard let stmt else { return nil }
        if sqlite3_column_type(stmt, idx) == SQLITE_NULL { return nil }
        return sqlite3_column_double(stmt, idx)
    }

    private static func bytesToMBps(_ b: Int64) -> Double {
        Double(Swift.max(b, 0)) / 1_048_576.0
    }
}

struct HistoryPoint: Sendable, Equatable {
    let t: Date
    let cpu: Double?
    let mem: Double?
    let disk: Double?
    let net: Double?
    let rxMBps: Double?
    let txMBps: Double?
}
