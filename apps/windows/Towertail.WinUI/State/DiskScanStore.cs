using System.IO.Compression;
using System.Text;
using Microsoft.Data.Sqlite;

namespace Towertail.WinUI.State;

public enum CleanupPickKind
{
    /// <summary>A folder or file that the user added. The key is the absolute path.</summary>
    Mine,
    /// <summary>A hidden recommendation. The key is <c>&lt;rule id&gt;|&lt;rec path&gt;</c>.</summary>
    Hidden,
}

/// <summary>A change of the user to the cleanup list, kept per node and scope.</summary>
public sealed record CleanupPick(CleanupPickKind Kind, string Key, DateTime AddedAt, bool On = true)
{
    public static string HiddenKey(DiskRec rec) => rec.RuleId + "|" + rec.Path;

    internal static string KindText(CleanupPickKind kind) => kind == CleanupPickKind.Mine ? "mine" : "hidden";
}

/// <summary>
/// Keeps the last <see cref="KeepPerScope"/> scan results for each node and scope in SQLite.
/// Same tables as the Mac DiskScanStore: <c>disk_scan(node_id, scope, finished_at, duration_ms, result_gz)</c>
/// and <c>disk_cleanup_pick</c> for the cleanup picks of the user.
/// One connection behind a lock: scans are rare and the rows are few.
/// </summary>
public sealed class DiskScanStore : IDisposable
{
    public const int KeepPerScope = 3;
    public const string FileName = "disk-scans.sqlite";

    private readonly SqliteConnection _conn;
    private readonly object _lock = new();

    public sealed record Entry(DateTime FinishedAt, long DurationMs, DiskScanResult Result);

    public DiskScanStore(string path)
    {
        var dir = Path.GetDirectoryName(path);
        if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
        SQLitePCL.Batteries_V2.Init();
        _conn = new SqliteConnection($"Data Source={path};Cache=Private;Pooling=False");
        _conn.Open();
        using var cmd = _conn.CreateCommand();
        cmd.CommandText = @"
            PRAGMA journal_mode=WAL;
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
            );";
        cmd.ExecuteNonQuery();
    }

    public void Save(Guid nodeId, string scope, DiskScanResult result)
    {
        var gz = Gzip(DiskScanCodec.Encode(result));
        lock (_lock)
        {
            using var tx = _conn.BeginTransaction();
            using (var ins = _conn.CreateCommand())
            {
                ins.CommandText = "INSERT INTO disk_scan (node_id, scope, finished_at, duration_ms, result_gz) VALUES ($id, $scope, $at, $ms, $gz)";
                ins.Parameters.AddWithValue("$id", nodeId.ToString());
                ins.Parameters.AddWithValue("$scope", scope);
                ins.Parameters.AddWithValue("$at", (result.FinishedAt - DateTime.UnixEpoch).TotalSeconds);
                ins.Parameters.AddWithValue("$ms", result.DurationMs);
                ins.Parameters.AddWithValue("$gz", gz);
                ins.ExecuteNonQuery();
            }
            using (var prune = _conn.CreateCommand())
            {
                prune.CommandText = @"
                    DELETE FROM disk_scan WHERE node_id = $id AND scope = $scope AND rowid NOT IN (
                        SELECT rowid FROM disk_scan WHERE node_id = $id AND scope = $scope
                        ORDER BY finished_at DESC, rowid DESC LIMIT $keep)";
                prune.Parameters.AddWithValue("$id", nodeId.ToString());
                prune.Parameters.AddWithValue("$scope", scope);
                prune.Parameters.AddWithValue("$keep", KeepPerScope);
                prune.ExecuteNonQuery();
            }
            tx.Commit();
        }
    }

    /// <summary>Puts <paramref name="result"/> in the newest row of the scope. The time and the older rows stay.</summary>
    public void ReplaceLatest(Guid nodeId, string scope, DiskScanResult result)
    {
        var gz = Gzip(DiskScanCodec.Encode(result));
        lock (_lock)
        {
            using var cmd = _conn.CreateCommand();
            cmd.CommandText = @"
                UPDATE disk_scan SET result_gz = $gz WHERE rowid = (
                    SELECT rowid FROM disk_scan WHERE node_id = $id AND scope = $scope
                    ORDER BY finished_at DESC, rowid DESC LIMIT 1)";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$scope", scope);
            cmd.Parameters.AddWithValue("$gz", gz);
            cmd.ExecuteNonQuery();
        }
    }

    /// <summary>The cleanup picks of the scope, the oldest first.</summary>
    public IReadOnlyList<CleanupPick> Picks(Guid nodeId, string scope)
    {
        var list = new List<CleanupPick>();
        lock (_lock)
        {
            using var cmd = _conn.CreateCommand();
            cmd.CommandText = "SELECT kind, key, on_flag, added_at FROM disk_cleanup_pick WHERE node_id = $id AND scope = $scope ORDER BY added_at, rowid";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$scope", scope);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
            {
                CleanupPickKind? kind = reader.GetString(0) switch
                {
                    "mine" => CleanupPickKind.Mine,
                    "hidden" => CleanupPickKind.Hidden,
                    _ => null,
                };
                if (kind is null) continue;
                list.Add(new CleanupPick(kind.Value, reader.GetString(1),
                    DateTime.UnixEpoch.AddSeconds(reader.GetDouble(3)), reader.GetInt64(2) != 0));
            }
        }
        return list;
    }

    /// <summary>Adds the pick, or sets its on flag when it is there.</summary>
    public void SavePick(Guid nodeId, string scope, CleanupPick pick)
    {
        lock (_lock)
        {
            using var cmd = _conn.CreateCommand();
            cmd.CommandText = @"
                INSERT INTO disk_cleanup_pick (node_id, scope, kind, key, on_flag, added_at) VALUES ($id, $scope, $kind, $key, $on, $at)
                ON CONFLICT (node_id, scope, kind, key) DO UPDATE SET on_flag = excluded.on_flag";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$scope", scope);
            cmd.Parameters.AddWithValue("$kind", CleanupPick.KindText(pick.Kind));
            cmd.Parameters.AddWithValue("$key", pick.Key);
            cmd.Parameters.AddWithValue("$on", pick.On ? 1 : 0);
            cmd.Parameters.AddWithValue("$at", (pick.AddedAt.ToUniversalTime() - DateTime.UnixEpoch).TotalSeconds);
            cmd.ExecuteNonQuery();
        }
    }

    public void RemovePick(Guid nodeId, string scope, CleanupPickKind kind, string key)
    {
        lock (_lock)
        {
            using var cmd = _conn.CreateCommand();
            cmd.CommandText = "DELETE FROM disk_cleanup_pick WHERE node_id = $id AND scope = $scope AND kind = $kind AND key = $key";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$scope", scope);
            cmd.Parameters.AddWithValue("$kind", CleanupPick.KindText(kind));
            cmd.Parameters.AddWithValue("$key", key);
            cmd.ExecuteNonQuery();
        }
    }

    /// <summary>The kept results, the newest first. Rows that do not decode are left out.</summary>
    public IReadOnlyList<Entry> List(Guid nodeId, string scope)
    {
        var list = new List<Entry>();
        lock (_lock)
        {
            using var cmd = _conn.CreateCommand();
            cmd.CommandText = "SELECT finished_at, duration_ms, result_gz FROM disk_scan WHERE node_id = $id AND scope = $scope ORDER BY finished_at DESC, rowid DESC";
            cmd.Parameters.AddWithValue("$id", nodeId.ToString());
            cmd.Parameters.AddWithValue("$scope", scope);
            using var reader = cmd.ExecuteReader();
            while (reader.Read())
            {
                try
                {
                    var result = DiskScanCodec.DecodeResult(Gunzip((byte[])reader.GetValue(2)));
                    list.Add(new Entry(DateTime.UnixEpoch.AddSeconds(reader.GetDouble(0)), reader.GetInt64(1), result));
                }
                catch { /* a bad row must not hide the others */ }
            }
        }
        return list;
    }

    public Entry? Latest(Guid nodeId, string scope) => List(nodeId, scope).FirstOrDefault();

    private static byte[] Gzip(string text)
    {
        using var ms = new MemoryStream();
        using (var gz = new GZipStream(ms, CompressionLevel.Optimal, leaveOpen: true))
            gz.Write(Encoding.UTF8.GetBytes(text));
        return ms.ToArray();
    }

    private static string Gunzip(byte[] data)
    {
        using var gz = new GZipStream(new MemoryStream(data), CompressionMode.Decompress);
        using var reader = new StreamReader(gz, Encoding.UTF8);
        return reader.ReadToEnd();
    }

    public void Dispose()
    {
        lock (_lock) _conn.Dispose();
    }
}
