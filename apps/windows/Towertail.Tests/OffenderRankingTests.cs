using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class OffenderRankingTests
{
    private static readonly DateTime T0 = new(2026, 11, 25, 12, 0, 0, DateTimeKind.Utc);

    private static ProcSample P(int pid, string name, double cpu, long rss = 100)
        => new(pid, 1, name, "/bin/" + name, "me", cpu, rss, 1, "R", null, null, null);

    private static ProcSeries.Snapshot Snap(int sec, params ProcSample[] items) => new(T0.AddSeconds(sec), false, items);

    [Fact]
    public void RanksByAverageOverWindow()
    {
        var snaps = new[]
        {
            Snap(0, P(1, "spiky", 90), P(2, "steady", 40)),
            Snap(10, P(1, "spiky", 0), P(2, "steady", 40)),
            Snap(20, P(1, "spiky", 0), P(2, "steady", 40)),
        };
        var top = new OffenderRanking(snaps, (T0, T0.AddSeconds(30)), ProcSort.Cpu).Top(5);
        top.Select(o => o.Ref.Name).Should().Equal("steady", "spiky");
        top[0].Avg.Should().BeApproximately(40, 0.001);
        top[1].Peak.Should().Be(90);
        top[1].HighFor.Should().Be(TimeSpan.FromSeconds(10));
        top[0].Series.Should().HaveCount(60);
    }

    [Fact]
    public void GroupsPidsOfSameCommand()
    {
        var snaps = new[] { Snap(0, P(1, "worker", 30, 10), P(2, "worker", 25, 20), P(3, "other", 10)) };
        var top = new OffenderRanking(snaps, (T0, T0.AddSeconds(30)), ProcSort.Cpu).Top(5);
        top.Should().HaveCount(2);
        top[0].Ref.Name.Should().Be("worker");
        top[0].Avg.Should().Be(55);
        top[0].RssMax.Should().Be(30);
    }

    [Fact]
    public void SortsByMemoryWhenAsked()
    {
        var snaps = new[] { Snap(0, P(1, "cpu", 90, 10), P(2, "mem", 1, 5_000)) };
        new OffenderRanking(snaps, (T0, T0.AddSeconds(30)), ProcSort.Rss).Top(1)[0].Ref.Name.Should().Be("mem");
    }
}
