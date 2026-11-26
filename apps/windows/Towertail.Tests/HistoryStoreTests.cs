using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class HistoryStoreTests : IAsyncLifetime
{
    private string _path = "";
    private HistoryStore _store = null!;

    public ValueTask InitializeAsync()
    {
        _path = Path.Combine(Path.GetTempPath(), $"tt-test-{Guid.NewGuid():N}.sqlite");
        _store = new HistoryStore(_path);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _store.DisposeAsync();
        if (File.Exists(_path)) File.Delete(_path);
    }

    [Fact]
    public void AppendAndLoadRoundTrips()
    {
        var node = Guid.NewGuid();
        for (int i = 0; i < 100; i++)
            _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-100 + i), 10 + i, 20, 30, 5, 1.0, 1.5));

        var rows = _store.LoadRecent(node, limit: 200);
        rows.Should().HaveCount(100);
        rows[0].T.Should().BeBefore(rows[^1].T);
    }

    [Fact]
    public async Task ProcsRoundTripsAndSurvivesReopen()
    {
        var node = Guid.NewGuid();
        _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-2), 1, 2, 3, 4, 5, 6, Procs: 34983));
        _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-1), 1, 2, 3, 4, 5, 6));
        // The read waits for the queued writes. DisposeAsync can drop them.
        _store.LoadRecent(node, 1);

        // Reopen: the procs ALTER runs again and must ignore the duplicate column.
        await _store.DisposeAsync();
        _store = new HistoryStore(_path);

        var rows = _store.LoadRecent(node, limit: 10);
        rows.Select(r => r.Procs).Should().Equal(34983, null);
    }

    [Fact]
    public void ScoreAndLoadRoundTrip()
    {
        var node = Guid.NewGuid();
        _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-1), 1, 2, 3, 4, 5, 6, Score: 64.5, Load: 2.25));
        var row = _store.LoadRecent(node, limit: 10).Single();
        row.Score.Should().Be(64.5);
        row.Load.Should().Be(2.25);
    }

    [Fact]
    public void LoadRangeAveragesToMaxPoints()
    {
        var node = Guid.NewGuid();
        var from = DateTime.UtcNow.AddMinutes(-10);
        for (int i = 0; i < 600; i++)
            _store.Append(node, new HistoryPoint(from.AddSeconds(i), i < 300 ? 10 : 30, null, null, null, null, null, Score: 90, Load: 1));

        var rows = _store.LoadRange(node, from, from.AddMinutes(10), 10);

        rows.Should().HaveCount(10);
        rows[0].Cpu.Should().BeApproximately(10, 0.001);
        rows[^1].Cpu.Should().BeApproximately(30, 0.001);
        rows[0].T.Should().BeBefore(rows[^1].T);
        rows.Should().OnlyContain(r => r.Score == 90 && r.Load == 1);
        _store.LoadRange(node, from, from, 10).Should().BeEmpty();
    }

    [Fact]
    public async Task MigrationAddsScoreAndLoadColumns()
    {
        // A database from before the workbench has no health_score and load_1 columns.
        await _store.DisposeAsync();
        File.Delete(_path);
        using (var old = new Microsoft.Data.Sqlite.SqliteConnection($"Data Source={_path};Pooling=False"))
        {
            old.Open();
            using var cmd = old.CreateCommand();
            cmd.CommandText = "CREATE TABLE samples (node_id TEXT NOT NULL, ts REAL NOT NULL, cpu REAL, mem REAL, disk REAL, net REAL, rx_mbps REAL, tx_mbps REAL, procs REAL); INSERT INTO samples (node_id, ts, cpu) VALUES ('" + Guid.Empty + "', 1000, 5)";
            cmd.ExecuteNonQuery();
        }
        _store = new HistoryStore(_path);
        var node = Guid.NewGuid();
        _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-1), 1, 2, 3, 4, 5, 6, Score: 80, Load: 3));

        _store.LoadRecent(node, limit: 5).Single().Score.Should().Be(80);
        var old1 = _store.LoadRecent(Guid.Empty, limit: 5).Single();
        old1.Cpu.Should().Be(5);
        old1.Score.Should().BeNull();
    }

    [Fact]
    public async Task PruneRemovesExpiredRows()
    {
        var node = Guid.NewGuid();
        var now = DateTime.UtcNow;
        var proc = new ProcSample(7, 1, "node", null, "u", 1, 1, 1, null, null, null, null);
        foreach (var t in new[] { now.AddHours(-3), now.AddMinutes(-1) })
        {
            _store.AppendProcs(node, t, false, new[] { proc });
            _store.AppendDiskCapacity(node, t, new[] { new DiskSample("C:", "NTFS", 1, 2) });
            _store.AppendDiskIO(node, t, 1, 1, null);
        }

        await _store.PruneAsync(now);

        using var conn = new Microsoft.Data.Sqlite.SqliteConnection($"Data Source={_path};Pooling=False");
        conn.Open();
        foreach (var table in new[] { "proc_snapshots", "disk_capacity", "disk_io" })
        {
            using var cmd = conn.CreateCommand();
            cmd.CommandText = $"SELECT COUNT(*) FROM {table}";
            ((long)cmd.ExecuteScalar()!).Should().Be(1, table);
        }
    }
}
