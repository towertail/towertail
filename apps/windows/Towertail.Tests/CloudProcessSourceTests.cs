using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudProcessSourceTests
{
    private static readonly DateTime T0 = new(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc);
    private readonly Guid _host = Guid.NewGuid();

    private (CloudProcessSource Source, FakeHistorySource Fake) Make(RemoteProcessDetail detail)
    {
        var fake = new FakeHistorySource { Detail = detail };
        return (new CloudProcessSource(_host, new CloudHistoryCache(fake, new StepClock(T0)), new StepClock(T0)), fake);
    }

    private static RemoteProcessDetail Detail(RemoteProcessFacts? facts) => new()
    {
        Key = "k",
        Repo = "towertail",
        Points = [new RemoteProcPoint(T0.AddMinutes(-1), 50, 2048, 1000), new RemoteProcPoint(T0, 60, 4096)],
        Facts = facts,
    };

    [Fact]
    public async Task MapsPointsAndFacts()
    {
        var facts = new RemoteProcessFacts
        {
            SeenAt = T0.AddMinutes(-1), Pid = 7, Ppid = 1, Parent = "zsh", User = "root", Cwd = "/src", Threads = 4,
            ListenTcp = [8080], ListenUdp = [53],
        };
        var (source, _) = Make(Detail(facts));

        var d = await source.DetailAsync(new ProcessRef("k", null, "go", "go test"), (T0.AddHours(-1), T0), TestContext.Current.CancellationToken);

        d.Cpu.Select(p => p.Value).Should().Equal(50, 60);
        d.Rss.Select(p => p.Value).Should().Equal(2048, 4096);
        d.Io.Should().ContainSingle().Which.Value.Should().Be(1000);
        d.Facts.Pid.Should().Be(7);
        d.Facts.Ports.Should().Equal("tcp 8080", "udp 53");
        d.Facts.Repo.Should().Be("towertail");
        d.Facts.Stale.Should().BeFalse();
    }

    [Fact]
    public async Task OldFactsAreStale()
    {
        var facts = new RemoteProcessFacts { SeenAt = T0.AddHours(-2), Pid = 7 };
        var (source, _) = Make(Detail(facts));

        var d = await source.DetailAsync(new ProcessRef("k", null, "go", "go test"), (T0.AddHours(-3), T0.AddHours(-2)), TestContext.Current.CancellationToken);

        d.Facts.Stale.Should().BeTrue();
    }

    [Fact]
    public async Task NoFactsAreStale()
    {
        var (source, _) = Make(Detail(null));

        var d = await source.DetailAsync(new ProcessRef("k", null, "go", "go test"), (T0.AddDays(-3), T0.AddDays(-2)), TestContext.Current.CancellationToken);

        d.Facts.Stale.Should().BeTrue();
        d.Facts.Repo.Should().Be("towertail");
    }
}
