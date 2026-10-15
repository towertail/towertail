using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class MetricSeriesTests
{
    [Fact]
    public void AppendsAndReports()
    {
        var series = new MetricSeries();
        var t0 = DateTime.UtcNow;
        series.Append(t0, 42);
        series.Append(t0.AddSeconds(1), 50);
        series.Latest.Should().Be(50);
        series.Points.Should().HaveCount(2);
    }

    [Fact]
    public void TrimsOlderThanRetention()
    {
        var series = new MetricSeries();
        var tOld = DateTime.UtcNow - MetricSeries.Retention - TimeSpan.FromMinutes(1);
        series.Append(tOld, 1);
        series.Append(DateTime.UtcNow, 2);
        series.Points.Should().HaveCount(1);
        series.Points[0].Value.Should().Be(2);
    }
}
