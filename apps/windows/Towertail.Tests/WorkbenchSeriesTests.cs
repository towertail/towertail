using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class WorkbenchSeriesTests
{
    private static readonly DateTime T0 = new(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc);
    private readonly Guid _host = Guid.NewGuid();
    private readonly FakeHistorySource _source = new();
    private readonly WorkbenchSeries _series;
    private readonly (DateTime From, DateTime To) _window = (T0.AddHours(-24), T0);

    public WorkbenchSeriesTests() => _series = new WorkbenchSeries(new CloudHistoryCache(_source, new StepClock(T0)));

    private static readonly TimeSpan Step = TimeSpan.FromMinutes(5);

    [Theory]
    [InlineData(HostTab.Overview, true, 16)]
    [InlineData(HostTab.Overview, false, 14)]
    [InlineData(HostTab.Health, true, 11)]
    [InlineData(HostTab.Disks, true, 4)]
    [InlineData(HostTab.Processes, true, 0)]
    public void MetricsPerTab(HostTab tab, bool linux, int count)
    {
        var names = WorkbenchSeries.Metrics(tab, linux);
        names.Should().HaveCount(count);
        names.Count.Should().BeLessThanOrEqualTo(16);
    }

    [Fact]
    public async Task OneRequestForEachTabAndWindow()
    {
        await _series.LoadAsync(_host, HostTab.Overview, _window, Step, true, TestContext.Current.CancellationToken);
        await _series.LoadAsync(_host, HostTab.Overview, _window, Step, true, TestContext.Current.CancellationToken);
        _source.HistoryCalls.Should().Be(1);

        await _series.LoadAsync(_host, HostTab.Disks, _window, Step, true, TestContext.Current.CancellationToken);
        _source.HistoryCalls.Should().Be(2);
        _source.DiskCalls.Should().Be(1);

        await _series.LoadAsync(_host, HostTab.Processes, _window, Step, true, TestContext.Current.CancellationToken);
        _source.HistoryCalls.Should().Be(2);
    }

    [Fact]
    public async Task ErrorIsReported()
    {
        _source.Fail = new InvalidOperationException("plan limit");

        await _series.LoadAsync(_host, HostTab.Overview, _window, Step, true, TestContext.Current.CancellationToken);

        _series.Error.Should().Be("plan limit");
        _series.Loading.Should().BeFalse();
    }

    [Fact]
    public async Task ConvertsUnits()
    {
        _source.Series = new()
        {
            ["mem"] = [new RemoteHistoryPoint(T0, 0.5)],
            ["net_rx"] = [new RemoteHistoryPoint(T0, 2_097_152)],
            ["cpu"] = [new RemoteHistoryPoint(T0, 42)],
        };

        await _series.LoadAsync(_host, HostTab.Overview, _window, Step, true, TestContext.Current.CancellationToken);

        _series.Points("mem", null).Single().Value.Should().Be(50);
        _series.Points("net_rx", null).Single().Value.Should().Be(2);
        _series.Points("cpu", null).Single().Value.Should().Be(42);
    }

    [Fact]
    public void LivePointsAppendAfterLastRestPoint()
    {
        var now = DateTime.UtcNow;
        var rest = new[] { new TimedValue(now.AddMinutes(-10), 1), new TimedValue(now.AddMinutes(-5), 2) };
        var live = new MetricSeries();
        live.Append(now.AddMinutes(-7), 9);
        live.Append(now.AddMinutes(-2), 3);
        live.Append(now, 4);

        WorkbenchSeries.Append(rest, live).Select(p => p.Value).Should().Equal(1, 2, 3, 4);
        WorkbenchSeries.Append(rest, null).Should().Equal(rest);
    }

    [Fact]
    public async Task ScoreSkipsTimesWithNoChecks()
    {
        _source.Series = new()
        {
            ["cpu"] = [new RemoteHistoryPoint(T0, 10), new RemoteHistoryPoint(T0.AddMinutes(5), 99)],
            ["load"] = [new RemoteHistoryPoint(T0.AddMinutes(10), 1)],
        };
        await _series.LoadAsync(_host, HostTab.Health, _window, Step, true, TestContext.Current.CancellationToken);
        await _series.LoadAsync(_host, HostTab.Overview, _window, Step, true, TestContext.Current.CancellationToken);

        var score = _series.ScorePoints(MetricThresholds.Defaults, MutedSignals.None, true);

        score.Should().HaveCount(2);
        score[0].Value.Should().BeGreaterThan(score[1].Value);
    }
}
