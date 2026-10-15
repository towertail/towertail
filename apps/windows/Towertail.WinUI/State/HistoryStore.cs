using Microsoft.Data.Sqlite;
using System.Collections.Concurrent;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

/// <summary>
/// SQLite-backed ring of recent samples so the popover can show history after an app restart.
/// 4 tables mirror the Mac HistoryStore.swift schema verbatim — <c>samples</c>,
/// <c>proc_snapshots</c>, <c>disk_capacity</c>, <c>disk_io</c>. Writes are non-blocking,
/// serialized onto a dedicated background thread.
/// </summary>
public sealed class HistoryStore : IAsyncDisposable
{
    public const int MaxRowsPerNode = 2000;
    public static readonly TimeSpan ProcRetention = TimeSpan.FromHours(2);
    public static readonly TimeSpan DiskRetention = TimeSpan.FromHours(2);

    private readonly string _path;
    private readonly SqliteConnection _conn;
    private readonly BlockingCollection<Action<SqliteConnection>> _queue = new();
    private readonly Task _worker;
    private readonly CancellationTokenSource _cts = new();

    private static readonly JsonSerializerOptions ProcJson = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    public HistoryStore(string path)
    {
        _path = path;
        var dir = Path.GetDirectoryName(path);
        if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);

        // Shared-cache off; single connection on a single thread — mirrors the Mac
        // serial queue model and avoids SQLite's write-lock contention.
        SQLitePCL.Batteries_V2.Init();
        _conn = new SqliteConnection($"Data Source={path};Cache=Private;Pooling=False");
        _conn.Open();
        ExecuteMigrations();

        _worker = Task.Factory.StartNew(WorkerLoop,
            _cts.Token,
            TaskCreationOptions.LongRunning | TaskCreationOptions.DenyChildAttach,
            TaskScheduler.Default);
    }

    private void ExecuteMigrations()
    {
        const string sql = @"
        PRAGMA journal_mode=WAL;
        PRAGMA synchronous=NORMAL;

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
        ";
        using var cmd = _conn.CreateCommand();
        cmd.CommandText = sql;
        cmd.ExecuteNonQuery();

        // Added after v1 shipped. SQLite has no ADD COLUMN IF NOT EXISTS, so
        // ignore the "duplicate column" error on databases that have it.
        try
        {
            using var alter = _conn.CreateCommand();
            alter.CommandText = "ALTER TABLE samples ADD COLUMN procs REAL";
            alter.ExecuteNonQuery();
        }
        catch (SqliteException ex) when (ex.Message.Contains("duplicate column", StringComparison.OrdinalIgnoreCase)) { }
    }

    private void WorkerLoop()
    {
        foreach (var work in _queue.GetConsumingEnumerable(_cts.Token))
        {
            try { work(_conn); }
            catch { /* swallow — dropping a history write is better than crashing */ }
        }
    }

    private void Enqueue(Action<SqliteConnection> work)
    {
        if (!_queue.IsAddingCompleted) _queue.Add(work);
    }

    private static double ToEpoch(DateTime t)
        => (t.ToUniversalTime() - DateTime.UnixEpoch).TotalSeconds;

    public void Append(Guid nodeId, HistoryPoint point)
    {
        var id = nodeId.ToString();
        Enqueue(conn =>
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "INSERT INTO samples (node_id, ts, cpu, mem, disk, net, rx_mbps, tx_mbps, procs) VALUES ($id, $ts, $cpu, $mem, $disk, $net, $rx, $tx, $procs)";
            cmd.Parameters.AddWithValue("$id", id);
            cmd.Parameters.AddWithValue("$ts", ToEpoch(point.T));
            cmd.Parameters.AddWithValue("$cpu", (object?)point.Cpu ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$mem", (object?)point.Mem ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$disk", (object?)point.Disk ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$net", (object?)point.Net ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$rx", (object?)point.RxMBps ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$tx", (object?)point.TxMBps ?? DBNull.Value);
            cmd.Parameters.AddWithValue("$procs", (object?)point.Procs ?? DBNull.Value);
            cmd.ExecuteNonQuery();
        });
    }

    public IReadOnlyList<HistoryPoint> LoadRecent(Guid nodeId, int limit = MaxRowsPerNode)
    {
        var tcs = new TaskCompletionSource<IReadOnlyList<HistoryPoint>>();
        Enqueue(conn =>
        {
            var list = new List<HistoryPoint>();
            using var cmd = conn.CreateCommand();
            cmd.CommandText = @"
                SELECT ts, cpu, mem, disk, net, rx_mbps, tx_mbps, procs FROM samples
                WHERE node_id = $id ORDER BY ts DESC LIMIT $limit";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$limit", limit);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
            {
                list.Add(new HistoryPoint(
                    T: DateTime.UnixEpoch.AddSeconds(reader.GetDouble(0)),
                    Cpu: reader.IsDBNull(1) ? null : reader.GetDouble(1),
                    Mem: reader.IsDBNull(2) ? null : reader.GetDouble(2),
                    Disk: reader.IsDBNull(3) ? null : reader.GetDouble(3),
                    Net: reader.IsDBNull(4) ? null : reader.GetDouble(4),
                    RxMBps: reader.IsDBNull(5) ? null : reader.GetDouble(5),
                    TxMBps: reader.IsDBNull(6) ? null : reader.GetDouble(6),
                    Procs: reader.IsDBNull(7) ? null : reader.GetDouble(7)));
            }
            list.Reverse();
            tcs.TrySetResult(list);
        });
        return tcs.Task.GetAwaiter().GetResult();
    }

    public void Trim(Guid nodeId, int keep = MaxRowsPerNode)
    {
        var id = nodeId.ToString();
        Enqueue(conn =>
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = @"
                DELETE FROM samples
                WHERE node_id = $id
                  AND ts < (
                    SELECT MIN(ts) FROM (
                      SELECT ts FROM samples WHERE node_id = $id
                      ORDER BY ts DESC LIMIT $keep
                    )
                  )";
            cmd.Parameters.AddWithValue("$id", id);
            cmd.Parameters.AddWithValue("$keep", keep);
            cmd.ExecuteNonQuery();
        });
    }

    public void AppendProcs(Guid nodeId, DateTime t, bool root, IReadOnlyList<ProcSample> items)
    {
        var id = nodeId.ToString();
        var ts = ToEpoch(t);
        var payload = JsonSerializer.SerializeToUtf8Bytes(items, ProcJson);
        Enqueue(conn =>
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "INSERT INTO proc_snapshots (node_id, ts, root, items_json) VALUES ($id, $ts, $root, $items)";
            cmd.Parameters.AddWithValue("$id", id);
            cmd.Parameters.AddWithValue("$ts", ts);
            cmd.Parameters.AddWithValue("$root", root ? 1 : 0);
            cmd.Parameters.AddWithValue("$items", payload);
            cmd.ExecuteNonQuery();

            var cutoff = ts - ProcRetention.TotalSeconds;
            using var prune = conn.CreateCommand();
            prune.CommandText = "DELETE FROM proc_snapshots WHERE node_id = $id AND ts < $cutoff";
            prune.Parameters.AddWithValue("$id", id);
            prune.Parameters.AddWithValue("$cutoff", cutoff);
            prune.ExecuteNonQuery();
        });
    }

    public sealed record ProcHistoryRow(DateTime T, bool Root, IReadOnlyList<ProcSample> Items);

    public IReadOnlyList<ProcHistoryRow> LoadRecentProcs(Guid nodeId)
    {
        var tcs = new TaskCompletionSource<IReadOnlyList<ProcHistoryRow>>();
        var cutoff = ToEpoch(DateTime.UtcNow) - ProcRetention.TotalSeconds;
        Enqueue(conn =>
        {
            var list = new List<ProcHistoryRow>();
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "SELECT ts, root, items_json FROM proc_snapshots WHERE node_id = $id AND ts >= $cutoff ORDER BY ts ASC";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$cutoff", cutoff);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
            {
                var ts = reader.GetDouble(0);
                var root = reader.GetInt32(1) != 0;
                var bytes = (byte[])reader.GetValue(2);
                IReadOnlyList<ProcSample> items = Array.Empty<ProcSample>();
                try { items = JsonSerializer.Deserialize<IReadOnlyList<ProcSample>>(bytes, ProcJson) ?? Array.Empty<ProcSample>(); }
                catch { }
                list.Add(new ProcHistoryRow(DateTime.UnixEpoch.AddSeconds(ts), root, items));
            }
            tcs.TrySetResult(list);
        });
        return tcs.Task.GetAwaiter().GetResult();
    }

    public void AppendDiskCapacity(Guid nodeId, DateTime t, IReadOnlyList<DiskSample> mounts)
    {
        if (mounts.Count == 0) return;
        var id = nodeId.ToString();
        var ts = ToEpoch(t);
        var rows = mounts.Select(d =>
        {
            var frac = d.Total > 0 ? (double)d.Used / d.Total : 0.0;
            return (Mount: d.Mount, Frac: Math.Clamp(frac, 0, 1));
        }).ToList();
        Enqueue(conn =>
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "INSERT INTO disk_capacity (node_id, ts, mount, frac) VALUES ($id, $ts, $m, $f)";
            cmd.Parameters.AddWithValue("$id", id);
            cmd.Parameters.AddWithValue("$ts", ts);
            var pMount = cmd.Parameters.Add("$m", SqliteType.Text);
            var pFrac = cmd.Parameters.Add("$f", SqliteType.Real);
            foreach (var r in rows)
            {
                pMount.Value = r.Mount;
                pFrac.Value = r.Frac;
                cmd.ExecuteNonQuery();
            }
            var cutoff = ts - DiskRetention.TotalSeconds;
            using var prune = conn.CreateCommand();
            prune.CommandText = "DELETE FROM disk_capacity WHERE node_id = $id AND ts < $cutoff";
            prune.Parameters.AddWithValue("$id", id);
            prune.Parameters.AddWithValue("$cutoff", cutoff);
            prune.ExecuteNonQuery();
        });
    }

    public void AppendDiskIO(Guid nodeId, DateTime t, long totalReadBps, long totalWriteBps, IReadOnlyList<DiskIoDevice>? devices)
    {
        var id = nodeId.ToString();
        var ts = ToEpoch(t);
        var rows = new List<(string Device, double R, double W)>
        {
            ("", BytesToMBps(totalReadBps), BytesToMBps(totalWriteBps)),
        };
        if (devices != null)
            foreach (var d in devices)
                rows.Add((d.Name, BytesToMBps(d.ReadBps), BytesToMBps(d.WriteBps)));

        Enqueue(conn =>
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "INSERT INTO disk_io (node_id, ts, device, read_mbps, write_mbps) VALUES ($id, $ts, $d, $r, $w)";
            cmd.Parameters.AddWithValue("$id", id);
            cmd.Parameters.AddWithValue("$ts", ts);
            var pDev = cmd.Parameters.Add("$d", SqliteType.Text);
            var pR = cmd.Parameters.Add("$r", SqliteType.Real);
            var pW = cmd.Parameters.Add("$w", SqliteType.Real);
            foreach (var r in rows)
            {
                pDev.Value = r.Device;
                pR.Value = r.R;
                pW.Value = r.W;
                cmd.ExecuteNonQuery();
            }
            var cutoff = ts - DiskRetention.TotalSeconds;
            using var prune = conn.CreateCommand();
            prune.CommandText = "DELETE FROM disk_io WHERE node_id = $id AND ts < $cutoff";
            prune.Parameters.AddWithValue("$id", id);
            prune.Parameters.AddWithValue("$cutoff", cutoff);
            prune.ExecuteNonQuery();
        });
    }

    public sealed record DiskCapacityRow(DateTime T, string Mount, double Frac);
    public sealed record DiskIORow(DateTime T, string Device, double ReadMBps, double WriteMBps);

    public IReadOnlyList<DiskCapacityRow> LoadRecentDiskCapacity(Guid nodeId)
    {
        var tcs = new TaskCompletionSource<IReadOnlyList<DiskCapacityRow>>();
        var cutoff = ToEpoch(DateTime.UtcNow) - DiskRetention.TotalSeconds;
        Enqueue(conn =>
        {
            var list = new List<DiskCapacityRow>();
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "SELECT ts, mount, frac FROM disk_capacity WHERE node_id = $id AND ts >= $cutoff ORDER BY ts ASC";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$cutoff", cutoff);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
                list.Add(new DiskCapacityRow(
                    DateTime.UnixEpoch.AddSeconds(reader.GetDouble(0)),
                    reader.GetString(1),
                    reader.GetDouble(2)));
            tcs.TrySetResult(list);
        });
        return tcs.Task.GetAwaiter().GetResult();
    }

    public IReadOnlyList<DiskIORow> LoadRecentDiskIO(Guid nodeId)
    {
        var tcs = new TaskCompletionSource<IReadOnlyList<DiskIORow>>();
        var cutoff = ToEpoch(DateTime.UtcNow) - DiskRetention.TotalSeconds;
        Enqueue(conn =>
        {
            var list = new List<DiskIORow>();
            using var cmd = conn.CreateCommand();
            cmd.CommandText = "SELECT ts, device, read_mbps, write_mbps FROM disk_io WHERE node_id = $id AND ts >= $cutoff ORDER BY ts ASC";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$cutoff", cutoff);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
                list.Add(new DiskIORow(
                    DateTime.UnixEpoch.AddSeconds(reader.GetDouble(0)),
                    reader.GetString(1),
                    reader.GetDouble(2),
                    reader.GetDouble(3)));
            tcs.TrySetResult(list);
        });
        return tcs.Task.GetAwaiter().GetResult();
    }

    private static double BytesToMBps(long b) => Math.Max(0, b) / 1_048_576.0;

    public async ValueTask DisposeAsync()
    {
        _queue.CompleteAdding();
        _cts.Cancel();
        try { await _worker.ConfigureAwait(false); } catch { }
        _conn.Dispose();
        _cts.Dispose();
    }
}

public sealed record HistoryPoint(
    DateTime T,
    double? Cpu,
    double? Mem,
    double? Disk,
    double? Net,
    double? RxMBps,
    double? TxMBps,
    double? Procs = null);
