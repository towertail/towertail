using System.IO.Compression;
using System.Text;
using Microsoft.Data.Sqlite;

namespace Towertail.WinUI.State;

/// <summary>
/// Keeps the last <see cref="KeepPerScope"/> scan results for each node and scope in SQLite.
/// Same table as the Mac DiskScanStore: <c>disk_scan(node_id, scope, finished_at, duration_ms, result_gz)</c>.
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
            CREATE INDEX IF NOT EXISTS idx_disk_scan_node_scope ON disk_scan(node_id, scope, finished_at);";
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
