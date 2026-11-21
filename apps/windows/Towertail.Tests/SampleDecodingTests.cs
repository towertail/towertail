using FluentAssertions;
using Towertail.WinUI.State;
using Towertail.Tests.Helpers;
using Xunit;

namespace Towertail.Tests;

public sealed class SampleDecodingTests
{
    [Fact]
    public void DecodesGoldenSample()
    {
        var json = ReleaseFixture.ReadSampleV1();
        var sample = SampleCodec.Decode(json);

        sample.V.Should().Be(1);
        sample.Host.Name.Should().Be("db-primary");
        sample.Host.Os.Should().Be("linux");
        sample.Host.Arch.Should().Be("arm64");
        sample.Cpu.Pct.Should().BeApproximately(42.3, 0.0001);
        sample.Cpu.Cores.Should().Be(8);
        sample.Mem.Total.Should().Be(16777216000);
        sample.Disks.Should().HaveCount(2);
        sample.Net.Should().NotBeNull();
        sample.Net!.RxBps.Should().Be(3355443);
        sample.Procs.Should().NotBeNull();
        sample.Procs!.Items.Should().HaveCount(2);
        sample.Procs.Items[0].ReadBytes.Should().Be(58720256);
        sample.Procs.Items[1].ReadBytes.Should().BeNull("omitted read_bytes should decode as null, not 0");
        sample.Procs.Items[0].Parent.Should().Be("systemd");
        sample.Procs.Items[0].ReadBps.Should().Be(1048576);
        sample.Procs.Items[0].WriteBps.Should().Be(262144);
        sample.Procs.Items[1].Cwd.Should().Be("/srv/app/web");
        sample.Procs.Items[1].Repo.Should().Be("/srv/app");
        sample.Procs.Items[1].Parent.Should().Be("pm2");
        sample.Procs.Items[1].ReadBps.Should().BeNull();
        sample.DiskIo!.Devices.Should().HaveCount(1);
        sample.Ports!.Items.Should().HaveCount(2);
        sample.Health!.Zombies.Should().Be(219);
        sample.Health.MemPressurePct.Should().Be(58);
    }

    [Fact]
    public void DecodesProcFolderFields()
    {
        var json = """{"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"procs":{"total":2,"visible":2,"root":false,"top_n":20,"items":[{"pid":812,"ppid":700,"name":"node","cpu_pct":85,"rss":1000,"cwd":"/home/u/.wt/sling-40/sling","repo":"/home/u/.wt/sling-40/sling","parent":"zsh","read_bps":0,"write_bps":4096},{"pid":1,"name":"init","cpu_pct":0,"rss":1}]},"errors":[]}""";
        var items = SampleCodec.Decode(json).Procs!.Items;

        items[0].Cwd.Should().Be("/home/u/.wt/sling-40/sling");
        items[0].Repo.Should().Be("/home/u/.wt/sling-40/sling");
        items[0].Parent.Should().Be("zsh");
        items[0].ReadBps.Should().Be(0, "a zero rate is not a missing rate");
        items[0].WriteBps.Should().Be(4096);
        items[1].Cwd.Should().BeNull();
        items[1].Repo.Should().BeNull();
        items[1].Parent.Should().BeNull();
        items[1].ReadBps.Should().BeNull();
    }

    [Fact]
    public void RejectsNewerSchemaVersion()
    {
        var json = """{"v":2,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}""";
        var act = () => SampleCodec.Decode(json);
        act.Should().Throw<Exception>();
    }

    [Fact]
    public void TolerantesUnknownFields()
    {
        var json = """{"v":1,"extra":"foo","ts":"2026-04-20T12:00:00.000Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x","future":true},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}""";
        var sample = SampleCodec.Decode(json);
        sample.V.Should().Be(1);
    }

    [Fact]
    public void AcceptsFractionalAndNonFractionalIsoTimestamps()
    {
        var withFrac = """{"v":1,"ts":"2026-04-20T12:00:00.500Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}""";
        var noFrac = """{"v":1,"ts":"2026-04-20T12:00:00Z","host":{"name":"h","os":"linux","arch":"arm64","kernel":"6","uptime_s":1,"sampler":"x"},"cpu":{"pct":0,"load_1":0,"load_5":0,"load_15":0,"cores":1},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}""";
        SampleCodec.Decode(withFrac).Ts.Millisecond.Should().Be(500);
        SampleCodec.Decode(noFrac).V.Should().Be(1);
    }
}
