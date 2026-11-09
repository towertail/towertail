using FluentAssertions;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

// The same cases as the stale-data tests of the Mac app (TOWERTAIL-106).
public sealed class StaleDataTests : IAsyncLifetime
{
    // After the VM creation time, which is the first stale baseline.
    private static readonly DateTime T0 = DateTime.UtcNow.AddHours(1);
    private static readonly TimeSpan Limit = TimeSpan.FromSeconds(30);

    private string _settingsPath = "";
    private string _historyPath = "";
    private HistoryStore _history = null!;
    private NodeStore _nodes = null!;
    private ServerSettings _settings = null!;
    private ServerStore _servers = null!;

    public ValueTask InitializeAsync()
    {
        _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _historyPath = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        _history = new HistoryStore(_historyPath);
        _nodes = new NodeStore(_settingsPath);
        _settings = new ServerSettings(_settingsPath)
        {
            NotificationsEnabled = true,
            NotifyCritical = true,
            NotifyDebounceSeconds = 0,
        };
        _servers = new ServerStore(_nodes, _settings, _history);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _history.DisposeAsync();
        if (File.Exists(_settingsPath)) File.Delete(_settingsPath);
        if (File.Exists(_historyPath)) File.Delete(_historyPath);
    }

    [Fact]
    public void NeverConnectedHostIsNotFlagged()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.CheckStale(DateTime.UtcNow.AddHours(1), Limit).Should().BeFalse();
        vm.NoDataSince.Should().BeNull();
    }

    [Fact]
    public void FlagsOncePerEpisodeAndIngestClears()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.Ingest(Make(T0), receivedAt: T0);

        vm.CheckStale(T0.AddSeconds(30), Limit).Should().BeFalse();
        vm.CheckStale(T0.AddSeconds(31), Limit).Should().BeTrue();
        vm.NoDataSince.Should().Be(T0.AddSeconds(31));
        vm.OfflineReason.Should().Be("no data for 31s");

        vm.CheckStale(T0.AddMinutes(3), Limit).Should().BeFalse();
        vm.NoDataSince.Should().Be(T0.AddSeconds(31));
        vm.OfflineReason.Should().Be("no data for 3m");

        vm.Ingest(Make(T0), receivedAt: T0.AddMinutes(4));
        vm.NoDataSince.Should().BeNull();
        vm.OfflineReason.Should().BeNull();
        vm.CheckStale(T0.AddMinutes(4).AddSeconds(31), Limit).Should().BeTrue();
    }

    [Fact]
    public void UsesLocalClockNotSampleTime()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        // The host clock is one hour behind.
        vm.Ingest(Make(T0.AddHours(-1)), receivedAt: T0);
        vm.CheckStale(T0.AddSeconds(10), Limit).Should().BeFalse();
    }

    [Fact]
    public void ReasonAddsTheLastError()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.Ingest(Make(T0), receivedAt: T0);
        vm.MarkOffline("timeout");
        vm.OfflineReason.Should().Be("timeout");

        vm.CheckStale(T0.AddMinutes(3), Limit).Should().BeTrue();
        vm.OfflineReason.Should().Be("no data for 3m (timeout)");

        // The watchdog keeps its text while no data arrives.
        vm.MarkOffline("connection refused");
        vm.OfflineReason.Should().Be("no data for 3m (timeout)");
        vm.CheckStale(T0.AddMinutes(4), Limit);
        vm.OfflineReason.Should().Be("no data for 4m (connection refused)");
    }

    [Fact]
    public void BaselineResetDelaysTheCheck()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        vm.Ingest(Make(T0), receivedAt: T0);
        vm.ResetStaleBaseline(T0.AddHours(2));
        vm.CheckStale(T0.AddHours(2).AddSeconds(20), Limit).Should().BeFalse();
        vm.CheckStale(T0.AddHours(2).AddSeconds(31), Limit).Should().BeTrue();
        vm.OfflineReason.Should().Be("no data for 31s");
    }

    [Theory]
    [InlineData(5, 30)]
    [InlineData(10, 30)]
    [InlineData(15, 45)]
    [InlineData(60, 180)]
    public void StaleLimitIsThreePollsAtLeast30s(int poll, int expected)
        => ServerStore.StaleLimit(poll).Should().Be(TimeSpan.FromSeconds(expected));

    [Fact]
    public void StoreFlagsAndNotifiesOnce()
    {
        var notifier = new ThresholdNotifier(_settings, _nodes);
        var raised = new List<ThresholdNotifier.NotifyPayload>();
        notifier.Raised += (_, p) => raised.Add(p);
        _servers.AttachNotifier(notifier);

        var n = _nodes.Nodes[0];
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        var vm = _servers.Find(n.Id)!;
        var last = vm.LastIngestAt!.Value;
        var limit = ServerStore.StaleLimit(_settings.PollingInterval(n.Kind));

        _servers.CheckStale(last.AddSeconds(5));
        raised.Should().BeEmpty();

        // Ticks each 5 s, so the store does not see a wake.
        for (var t = last.AddSeconds(10); t <= last + limit + TimeSpan.FromSeconds(30); t = t.AddSeconds(5))
            _servers.CheckStale(t);
        vm.NoDataSince.Should().NotBeNull();
        raised.Should().ContainSingle();
        raised[0].Metric.Should().Be(ThresholdNotifier.Metric.NoData);
        raised[0].Severity.Should().Be(ThresholdNotifier.Severity.Critical);
        raised[0].Body.Should().StartWith("No sample for ");
        _servers.AggregateLevel.Should().Be(HealthLevel.Nominal);

        // A new episode after the next sample notifies again.
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        var again = vm.LastIngestAt!.Value;
        _servers.CheckStale(again.AddSeconds(5));
        vm.NoDataSince.Should().BeNull();
        for (var t = again.AddSeconds(10); t <= again + limit + TimeSpan.FromSeconds(10); t = t.AddSeconds(5))
            _servers.CheckStale(t);
        raised.Should().HaveCount(2);
    }

    [Fact]
    public void StoreSkipsDisabledHosts()
    {
        var n = _nodes.Nodes[0];
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        _nodes.Update(n with { Enabled = false });
        var vm = _servers.Find(n.Id)!;
        var last = vm.LastIngestAt!.Value;
        for (var t = last.AddSeconds(5); t <= last.AddMinutes(5); t = t.AddSeconds(5))
            _servers.CheckStale(t);
        vm.NoDataSince.Should().BeNull();
    }

    [Fact]
    public void StoreWakeGapRestartsTheCheck()
    {
        var n = _nodes.Nodes[0];
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        var vm = _servers.Find(n.Id)!;
        var last = vm.LastIngestAt!.Value;
        _servers.CheckStale(last.AddSeconds(5));

        // The PC sleeps for one hour. The first tick after the wake must not flag.
        var wake = last.AddHours(1);
        _servers.CheckStale(wake);
        vm.NoDataSince.Should().BeNull();
        _servers.CheckStale(wake.AddSeconds(5));
        vm.NoDataSince.Should().BeNull();
    }

    [Fact]
    public void NotifierHonoursSnoozeAndSwitches()
    {
        var notifier = new ThresholdNotifier(_settings, _nodes);
        var count = 0;
        notifier.Raised += (_, _) => count++;

        var node = new Node { DisplayName = "n", Kind = NodeKind.Local, SnoozedUntil = DateTime.UtcNow.AddHours(1) };
        var vm = new ServerViewModel(node);
        vm.Ingest(Make(T0), receivedAt: T0);
        vm.CheckStale(T0.AddMinutes(3), Limit);
        notifier.EvaluateNoData(vm, T0.AddMinutes(3));
        count.Should().Be(0);

        var off = new ServerViewModel(new Node { DisplayName = "o", Kind = NodeKind.Local, NotifyOnCritical = false });
        off.Ingest(Make(T0), receivedAt: T0);
        off.CheckStale(T0.AddMinutes(3), Limit);
        notifier.EvaluateNoData(off, T0.AddMinutes(3));
        count.Should().Be(0);
    }

    [Fact]
    public void MarkUnreachableKeepsTheReason()
    {
        var n = _nodes.Nodes[0];
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        _servers.MarkUnreachable(n.Id, "timeout");
        var vm = _servers.Find(n.Id)!;
        vm.OfflineReason.Should().Be("timeout");
        vm.LastError.Should().Be("timeout");
        _servers.Ingest(n.Id, Make(DateTime.UtcNow));
        vm.OfflineReason.Should().BeNull();
    }

    [Fact]
    public async Task DeadlineMapsToTimeout()
    {
        var hang = new TaskCompletionSource<int>();
        var act = () => SshSamplerInvoker.WithDeadline(hang.Task, TimeSpan.FromMilliseconds(50), CancellationToken.None);
        (await act.Should().ThrowAsync<TimeoutException>()).Which.Message.Should().Be("timeout");
        hang.SetResult(1);
    }

    [Fact]
    public async Task DeadlinePassesResultsAndErrors()
    {
        (await SshSamplerInvoker.WithDeadline(Task.FromResult(7), TimeSpan.FromSeconds(1), CancellationToken.None)).Should().Be(7);
        var fail = () => SshSamplerInvoker.WithDeadline(Task.FromException<int>(new InvalidOperationException("x")), TimeSpan.FromSeconds(1), CancellationToken.None);
        await fail.Should().ThrowAsync<InvalidOperationException>();
    }

    [Fact]
    public void ShortDurationTexts()
    {
        ShortDuration.Text(TimeSpan.FromSeconds(42)).Should().Be("42s");
        ShortDuration.Text(TimeSpan.FromSeconds(180)).Should().Be("3m");
        ShortDuration.Text(TimeSpan.FromHours(68)).Should().Be("68h");
        ShortDuration.Age(TimeSpan.FromHours(68)).Should().Be("2d");
    }

    private static Sample Make(DateTime ts) => new(
        1, ts,
        new HostInfo("pc", "windows", "amd64", "10.0", 0, "s", null),
        new CpuInfo(10, 0, 0, 0, 8),
        new MemInfo(4_000_000_000, 16_000_000_000),
        new MemInfo(0, 0),
        new[] { new DiskSample("C:", "NTFS", 100_000_000_000, 500_000_000_000) },
        null, null, null, null, Array.Empty<string>());
}
