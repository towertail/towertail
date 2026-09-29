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
    public async Task AppendAndLoadRoundTrips()
    {
        var node = Guid.NewGuid();
        for (int i = 0; i < 100; i++)
            _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-100 + i), 10 + i, 20, 30, 5, 1.0, 1.5));

        // Allow the background worker to drain.
        await Task.Delay(100);

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
        await Task.Delay(100);

        // Reopen: the procs ALTER runs again and must ignore the duplicate column.
        await _store.DisposeAsync();
        _store = new HistoryStore(_path);

        var rows = _store.LoadRecent(node, limit: 10);
        rows.Select(r => r.Procs).Should().Equal(34983, null);
    }

    [Fact]
    public async Task TrimKeepsOnlyLatest()
    {
        var node = Guid.NewGuid();
        for (int i = 0; i < 50; i++)
            _store.Append(node, new HistoryPoint(DateTime.UtcNow.AddSeconds(-50 + i), i, null, null, null, null, null));
        await Task.Delay(100);
        _store.Trim(node, keep: 10);
        await Task.Delay(100);
        var rows = _store.LoadRecent(node, limit: 100);
        rows.Should().HaveCount(10);
    }
}
