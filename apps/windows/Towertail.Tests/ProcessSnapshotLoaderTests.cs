using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ProcessSnapshotLoaderTests
{
    private static readonly DateTime Now = new(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc);
    private static readonly TimeSpan Debounce = TimeSpan.FromMilliseconds(120);
    private static readonly TimeSpan LoadingAfter = TimeSpan.FromMilliseconds(80);

    private readonly Guid _host = Guid.NewGuid();
    private readonly FakeHistorySource _source = new();
    private readonly ManualDelay _delay = new();
    private readonly ProcessSnapshotLoader _loader;
    private readonly CloudHistoryCache _cache;

    public ProcessSnapshotLoaderTests()
    {
        _cache = new CloudHistoryCache(_source, new StepClock(Now));
        _loader = new ProcessSnapshotLoader(_host, _cache, delay: _delay.Func);
    }

    private static DateTime Old(int minutes) => Now.AddHours(-3).AddMinutes(-minutes);

    private static async Task Until(Func<bool> done)
    {
        for (var i = 0; i < 500 && !done(); i++) await Task.Delay(10);
        done().Should().BeTrue("the condition must hold within 5 s");
    }

    [Fact]
    public async Task BurstSendsOneRequest()
    {
        for (var i = 0; i < 8; i++) _loader.Request(Old(i * 3), ProcSort.Cpu, Now);
        _loader.Request(Old(30), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _source.SnapshotCalls.Should().Be(0);

        _delay.Release(Debounce);
        await Until(() => _loader.Snapshot is not null);

        _loader.Bucket!.Start.Should().Be(ProcBucket.Of(Old(30), Now).Start);
        _source.SnapshotStarts.Count(s => s == ProcBucket.Of(Old(30), Now).Start).Should().Be(1);
    }

    [Fact]
    public async Task CacheHitSkipsDebounce()
    {
        await _cache.ProcessesAtAsync(_host, ProcBucket.Of(Old(5), Now), ProcSort.Cpu);

        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _loader.Snapshot is not null);

        _delay.Pending.Should().NotContain(Debounce);
        _loader.LastRequest.Should().StartWith("cache hit");
    }

    [Fact]
    public async Task NewerRequestCancelsOlder()
    {
        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _loader.Request(Old(40), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Count(p => p == Debounce) == 1 && _delay.Pending.Count == 1);

        _delay.Release(Debounce);
        await Until(() => _loader.Snapshot is not null);

        _loader.Bucket!.Start.Should().Be(ProcBucket.Of(Old(40), Now).Start);
        _source.SnapshotStarts.Should().NotContain(ProcBucket.Of(Old(5), Now).Start);
    }

    [Fact]
    public async Task LoadingFlagOnlyAfter80Milliseconds()
    {
        _source.Gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _delay.Pending.Contains(LoadingAfter));
        _loader.ShowsLoading.Should().BeFalse();

        _delay.Release(LoadingAfter);
        await Until(() => _loader.ShowsLoading);

        _source.Gate.SetResult();
        await Until(() => _loader.Snapshot is not null);
        await Until(() => !_loader.ShowsLoading);
    }

    [Fact]
    public async Task FastAnswerNeverShowsLoading()
    {
        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _loader.Snapshot is not null);

        await Until(() => !_delay.Pending.Contains(LoadingAfter));
        _loader.ShowsLoading.Should().BeFalse();
    }

    [Fact]
    public async Task KeepsOldSnapshotWhileLoading()
    {
        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _loader.Snapshot is not null);
        var first = _loader.Snapshot;
        _source.Gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);

        _loader.Request(Old(500), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _delay.Pending.Contains(LoadingAfter));

        _loader.Snapshot.Should().BeSameAs(first);
        _source.Gate.SetResult();
        await Until(() => !ReferenceEquals(_loader.Snapshot, first));
    }

    [Fact]
    public async Task ErrorKeepsOldSnapshotAndSetsMessage()
    {
        _loader.Request(Old(5), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _loader.Snapshot is not null);
        var first = _loader.Snapshot;
        _source.Fail = new InvalidOperationException("down");

        _loader.Request(Old(500), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _loader.Error is not null);

        _loader.Error.Should().Be("down");
        _loader.Snapshot.Should().BeSameAs(first);
    }

    [Fact]
    public async Task PrefetchesTwoBucketsEachSideNotFuture()
    {
        var b = ProcBucket.Of(Old(30), Now);
        _loader.Request(Old(30), ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _source.SnapshotCalls == 5);

        _source.SnapshotStarts.Should().BeEquivalentTo(new[] { -2, -1, 0, 1, 2 }.Select(n => b.Shifted(n).Start));
    }

    [Fact]
    public async Task PrefetchSkipsBucketsInTheFuture()
    {
        var latest = ProcBucket.Of(Now, Now);
        _loader.Request(Now, ProcSort.Cpu, Now);
        await Until(() => _delay.Pending.Contains(Debounce));
        _delay.Release(Debounce);
        await Until(() => _source.SnapshotCalls == 3);
        await Task.Delay(100);

        _source.SnapshotStarts.Should().BeEquivalentTo(new[] { -2, -1, 0 }.Select(n => latest.Shifted(n).Start));
    }
}
