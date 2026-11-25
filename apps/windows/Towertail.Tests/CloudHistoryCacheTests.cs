using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudHistoryCacheTests
{
    private static readonly DateTime T0 = new(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc);
    private readonly Guid _host = Guid.NewGuid();
    private readonly FakeHistorySource _source = new();
    private readonly StepClock _clock = new(T0);
    private readonly CloudHistoryCache _cache;

    public CloudHistoryCacheTests() => _cache = new CloudHistoryCache(_source, _clock);

    private ProcBucket Closed(int n) => ProcBucket.Of(T0.AddHours(-3).AddMinutes(-n), T0);

    [Fact]
    public async Task LruEvictsOldestAt200()
    {
        for (var i = 0; i < CloudHistoryCache.SnapshotCapacity + 1; i++)
            await _cache.ProcessesAtAsync(_host, Closed(i), ProcSort.Cpu);

        _cache.TryGetProcessesAt(_host, Closed(0), ProcSort.Cpu).Should().BeNull();
        _cache.TryGetProcessesAt(_host, Closed(1), ProcSort.Cpu).Should().NotBeNull();
        _cache.TryGetProcessesAt(_host, Closed(200), ProcSort.Cpu).Should().NotBeNull();
    }

    [Fact]
    public async Task ReadKeepsEntryFromEviction()
    {
        for (var i = 0; i < CloudHistoryCache.SnapshotCapacity; i++)
            await _cache.ProcessesAtAsync(_host, Closed(i), ProcSort.Cpu);
        _cache.TryGetProcessesAt(_host, Closed(0), ProcSort.Cpu).Should().NotBeNull();

        await _cache.ProcessesAtAsync(_host, Closed(500), ProcSort.Cpu);

        _cache.TryGetProcessesAt(_host, Closed(0), ProcSort.Cpu).Should().NotBeNull();
        _cache.TryGetProcessesAt(_host, Closed(1), ProcSort.Cpu).Should().BeNull();
    }

    [Fact]
    public async Task ClosedBucketNeverExpires()
    {
        var b = Closed(0);
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        _clock.Advance(TimeSpan.FromDays(2));

        (await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu)).Should().NotBeNull();

        _source.SnapshotCalls.Should().Be(1);
    }

    [Fact]
    public async Task OpenBucketExpiresAfter10Seconds()
    {
        var b = ProcBucket.Of(T0, T0);
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);

        _clock.Advance(TimeSpan.FromSeconds(9));
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        _source.SnapshotCalls.Should().Be(1);

        _clock.Advance(TimeSpan.FromSeconds(1));
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        _source.SnapshotCalls.Should().Be(2);
    }

    [Fact]
    public async Task SortOrderHasOwnEntry()
    {
        var b = Closed(0);
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        await _cache.ProcessesAtAsync(_host, b, ProcSort.Rss);

        _source.SnapshotCalls.Should().Be(2);
    }

    [Fact]
    public async Task ConcurrentCallsShareOneRequest()
    {
        _source.Gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var b = Closed(0);

        var a = _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        var c = _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        _source.Gate.SetResult();
        await Task.WhenAll(a, c);

        _source.SnapshotCalls.Should().Be(1);
        a.Result.Should().BeSameAs(c.Result);
    }

    [Fact]
    public async Task CancelledCallerLeavesRequestRunning()
    {
        _source.Gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var b = Closed(0);
        using var cts = new CancellationTokenSource();

        var first = _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu, cts.Token);
        cts.Cancel();
        await first.Awaiting(t => t).Should().ThrowAsync<OperationCanceledException>();
        var second = _cache.ProcessesAtAsync(_host, b, ProcSort.Cpu);
        _source.Gate.SetResult();
        await second;

        _source.SnapshotCalls.Should().Be(1);
    }

    [Fact]
    public async Task ErrorIsNotCached()
    {
        _source.Fail = new InvalidOperationException("down");
        var act = async () => await _cache.OffendersAsync(_host, T0.AddHours(-1), T0, ProcSort.Cpu);
        await act.Should().ThrowAsync<InvalidOperationException>();

        _source.Fail = null;
        await _cache.OffendersAsync(_host, T0.AddHours(-1), T0, ProcSort.Cpu);

        _source.OffenderCalls.Should().Be(2);
    }

    [Fact]
    public async Task WindowRoundsToTheStep()
    {
        var step = TimeSpan.FromSeconds(60);
        await _cache.HistoryAsync(_host, ["cpu"], T0.AddHours(-1).AddSeconds(5), T0.AddSeconds(5), step);
        await _cache.HistoryAsync(_host, ["cpu"], T0.AddHours(-1).AddSeconds(40), T0.AddSeconds(40), step);
        _source.HistoryCalls.Should().Be(1);

        await _cache.HistoryAsync(_host, ["cpu"], T0.AddHours(-1).AddSeconds(65), T0.AddSeconds(65), step);
        await _cache.HistoryAsync(_host, ["cpu", "mem"], T0.AddHours(-1), T0, step);
        _source.HistoryCalls.Should().Be(3);
    }

    [Fact]
    public async Task EntryExpiresAfter60Seconds()
    {
        await _cache.DisksAsync(_host, T0.AddHours(-1), T0);
        _clock.Advance(TimeSpan.FromSeconds(59));
        await _cache.DisksAsync(_host, T0.AddHours(-1), T0);
        _source.DiskCalls.Should().Be(1);

        _clock.Advance(TimeSpan.FromSeconds(1));
        await _cache.DisksAsync(_host, T0.AddHours(-1), T0);
        _source.DiskCalls.Should().Be(2);
    }

    [Fact]
    public async Task ProcessEntryHasKeyInIt()
    {
        var w = (T0.AddHours(-1), T0);
        await _cache.ProcessAsync(_host, "a", w.Item1, w.Item2, TimeSpan.FromSeconds(10));
        await _cache.ProcessAsync(_host, "b", w.Item1, w.Item2, TimeSpan.FromSeconds(10));
        await _cache.ProcessAsync(_host, "a", w.Item1, w.Item2, TimeSpan.FromSeconds(10));

        _source.ProcessCalls.Should().Be(2);
    }
}
