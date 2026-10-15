using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ProcSeriesTests
{
    [Fact]
    public void LatestReflectsMostRecentAppend()
    {
        var series = new ProcSeries();
        var t0 = DateTime.UtcNow.AddSeconds(-10);
        series.Append(t0, false, new[]
        {
            new ProcSample(1, 0, "x", null, null, 1, 1, null, null, null, null, null)
        });
        var t1 = DateTime.UtcNow;
        series.Append(t1, false, new[]
        {
            new ProcSample(2, 0, "y", null, null, 2, 2, null, null, null, null, null)
        });
        series.Latest!.Items[0].Pid.Should().Be(2);
    }

    [Fact]
    public void AtReturnsSnapshotClosestButNotAfter()
    {
        var series = new ProcSeries();
        var t0 = DateTime.UtcNow.AddSeconds(-20);
        var t1 = DateTime.UtcNow.AddSeconds(-10);
        series.Append(t0, false, new[] { new ProcSample(1, 0, "a", null, null, 0, 0, null, null, null, null, null) });
        series.Append(t1, false, new[] { new ProcSample(2, 0, "b", null, null, 0, 0, null, null, null, null, null) });
        series.At(t0.AddSeconds(5))!.Items[0].Name.Should().Be("a");
        series.At(t1.AddSeconds(5))!.Items[0].Name.Should().Be("b");
    }
}
