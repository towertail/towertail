using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class OffenderFromRemoteTests
{
    private static readonly RemoteOffenders Response = JsonSerializer.Deserialize<RemoteOffenders>("""
        {"node_id":"00000000-0000-0000-0000-000000000001","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",
         "series_from":"2026-04-20T11:00:00Z","series_step_s":60,
         "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","repo":"towertail","cpu_pct_avg":12.5,"cpu_pct_max":90,"rss_max":2048,
          "series":{"cpu":[10,50,70,49],"rss":[1,2,3,4]}},
          {"key":"0a.Z28","name":"go","cmd":"go test","repo":"sling","cpu_pct_avg":1,"cpu_pct_max":2,"rss_max":1}]}
        """)!;

    [Fact]
    public void CountsPointsAtHalfACoreTimesStep()
    {
        var o = Offender.FromRemote(Response.Processes[0], Response);

        o.HighFor.Should().Be(TimeSpan.FromMinutes(2));
        o.Peak.Should().Be(90);
        o.Series.Should().Equal(10, 50, 70, 49);
    }

    [Fact]
    public void PointTimeUsesSeriesStart()
    {
        var o = Offender.FromRemote(Response.Processes[0], Response);
        var window = (Response.From.AddHours(-5), Response.To);

        o.TimeOf(2, window).Should().Be(new DateTime(2026, 4, 20, 11, 2, 0, DateTimeKind.Utc));
    }

    [Fact]
    public void SameKeyInTwoReposHasTwoIds()
    {
        var a = Offender.FromRemote(Response.Processes[0], Response);
        var b = Offender.FromRemote(Response.Processes[1], Response);

        a.Id.Should().NotBe(b.Id);
        b.HighFor.Should().Be(TimeSpan.Zero);
    }

    [Fact]
    public void LocalKeyUsesServerKeyWhenSet()
    {
        ProcessRef.KeyOf(new ProcSample(1, null, "go", "go test", null, 1, 1, null, null, null, null, null, Key: "srv")).Should().Be("srv");
    }
}
