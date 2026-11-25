using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class LocalProcessSourceTests
{
    private static ProcSample P(int pid, string name, double cpu, long rss, long? read = null, long? write = null, string? cwd = null)
        => new(pid, 1, name, "/bin/" + name, "me", cpu, rss, 4, "R", null, null, null, cwd, "repo", "launchd", read, write);

    [Fact]
    public async Task BuildsSeriesFromMatchingSnapshots()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        var t0 = DateTime.UtcNow.AddMinutes(-3);
        vm.Procs.Append(t0, false, new[] { P(1, "node", 50, 100, 10, 20), P(9, "other", 5, 1) });
        vm.Procs.Append(t0.AddMinutes(1), false, new[] { P(1, "node", 70, 150, 30, 40) });
        vm.Procs.Append(t0.AddMinutes(2), false, new[] { P(9, "other", 5, 1) });
        var r = ProcessRef.Local(P(1, "node", 0, 0));

        var d = await new LocalProcessSource(vm).DetailAsync(r, (t0.AddSeconds(-1), t0.AddMinutes(3)), CancellationToken.None);

        d.Cpu.Select(p => p.Value).Should().Equal(50, 70);
        d.Rss.Select(p => p.Value).Should().Equal(100, 150);
        d.Io.Select(p => p.Value).Should().Equal(30, 70);
    }

    [Fact]
    public async Task FactsFromNewestSample()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        var t0 = DateTime.UtcNow.AddMinutes(-2);
        vm.Procs.Append(t0, false, new[] { P(1, "node", 50, 100, cwd: "/old") });
        vm.Procs.Append(t0.AddMinutes(1), false, new[] { P(1, "node", 70, 150, cwd: "/new") });
        var r = ProcessRef.Local(P(1, "node", 0, 0));

        var d = await new LocalProcessSource(vm).DetailAsync(r, (t0.AddSeconds(-1), t0.AddMinutes(2)), CancellationToken.None);

        d.Facts.Cwd.Should().Be("/new");
        d.Facts.User.Should().Be("me");
        d.Facts.Parent.Should().Be("launchd");
        d.Facts.FactsAt.Should().Be(t0.AddMinutes(1));
    }

    [Fact]
    public async Task UnknownProcessGivesEmptyDetail()
    {
        var vm = new ServerViewModel(Node.LocalWindows("pc"));
        var d = await new LocalProcessSource(vm).DetailAsync(new ProcessRef("x", 1, "x", ""), (DateTime.UtcNow.AddHours(-1), DateTime.UtcNow), CancellationToken.None);
        d.Cpu.Should().BeEmpty();
        d.Facts.Should().Be(ProcessFacts.Empty);
    }
}
