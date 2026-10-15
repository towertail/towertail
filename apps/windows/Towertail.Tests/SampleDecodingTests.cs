using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class SampleDecodingTests
{
    [Fact]
    public void DecodesGoldenSample()
    {
        var json = File.ReadAllText(Path.Combine("TestData", "sample-v1.json"));
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
