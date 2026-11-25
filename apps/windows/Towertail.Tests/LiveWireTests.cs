using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class LiveWireTests
{
    private static readonly Guid A = Guid.Parse("00000000-0000-0000-0000-00000000000a");
    private static readonly Guid B = Guid.Parse("00000000-0000-0000-0000-00000000000b");

    [Fact]
    public void SummaryDecodesRatiosAndPercent()
    {
        var json = """
            {"ts":"2026-05-01T10:00:00.5Z","cpu":42.0,"mem":0.5,"disk":0.7,"rx":2048,"tx":1024,"load":1.25,"procs":310,
             "host":{"os":"linux","arch":"amd64","kernel":"6.8","cores":8,"mem_total":17179869184,"uptime_s":99,"sampler":"0.1.0+abc"}}
            """;
        var s = JsonSerializer.Deserialize<LiveSummary>(json, SampleCodec.Options)!;
        s.Cpu.Should().Be(42.0);
        s.Mem.Should().Be(0.5);
        s.Disk.Should().Be(0.7);
        s.Rx.Should().Be(2048);
        s.Load.Should().Be(1.25);
        s.Procs.Should().Be(310);
        s.Health.Should().BeNull();
        s.Host!.Cores.Should().Be(8);
        s.Host.MemTotal.Should().Be(17179869184);
        s.Ts.Should().Be(new DateTime(2026, 5, 1, 10, 0, 0, 500, DateTimeKind.Utc));
    }

    [Fact]
    public void HealthBriefReadsInodes()
    {
        var json = """{"ts":"2026-05-01T10:00:00Z","cpu":1,"mem":0.1,"disk":0.1,"rx":0,"tx":0,"load":0,"health":{"procs":120,"zombies":3,"pids_used":10,"pids_max":100,"inodes":0.91}}""";
        var s = JsonSerializer.Deserialize<LiveSummary>(json, SampleCodec.Options)!;
        s.Health!.Inodes.Should().Be(0.91);
        s.Health.Info.Procs.Should().Be(120);
        s.Health.Info.Zombies.Should().Be(3);
        s.Health.Info.PidsMax.Should().Be(100);
    }

    [Fact]
    public void ViewFrameMatchesWire()
    {
        new LiveView([A, B], new ViewDetail(A, DetailTab.Ports)).ToFrameJson().Should().Be(
            $$"""{"type":"view","hosts":["{{A}}","{{B}}"],"detail":{"host":"{{A}}","tab":"ports"},"ttl":60}""");
        LiveView.Empty.ToFrameJson().Should().Be("""{"type":"view","hosts":[],"ttl":60}""");
    }

    [Fact]
    public void ViewsAreEqualBySequenceDetailAndTtl()
    {
        new LiveView([A, B]).Should().Be(new LiveView([A, B]));
        new LiveView([A, B]).Should().NotBe(new LiveView([B, A]));
        new LiveView([A], new ViewDetail(A, DetailTab.Disks)).Should().NotBe(new LiveView([A]));
        new LiveView([A], Ttl: 30).Should().NotBe(new LiveView([A]));
    }

    [Theory]
    [InlineData("cpu", false, false, DetailTab.Processes)]
    [InlineData("mem", false, false, DetailTab.Processes)]
    [InlineData("procs", false, false, DetailTab.Processes)]
    [InlineData("net", false, false, DetailTab.Processes)]
    [InlineData("net", true, false, DetailTab.Ports)]
    [InlineData("disk", false, false, DetailTab.Disks)]
    [InlineData("disk", false, true, null)]
    [InlineData("health", false, false, DetailTab.Health)]
    [InlineData("other", false, false, null)]
    public void DetailTabMapFollowsMac(string tag, bool ports, bool usage, DetailTab? expected)
        => DetailTabMap.For(tag, ports, usage).Should().Be(expected);

    [Fact]
    public void LeaseNoticeKnowsTheLimit()
    {
        new LeaseNotice("ended", "limit").IsLimit.Should().BeTrue();
        new LeaseNotice("ended", null).IsLimit.Should().BeFalse();
    }
}
