using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests;

public sealed class RemoteHistoryDecodingTests
{
    private const string Node = "00000000-0000-0000-0000-000000000001";

    private static T Decode<T>(string json) => JsonSerializer.Deserialize<T>(json)!;

    [Fact]
    public void RawSnapshotHasPid()
    {
        var s = Decode<RemoteProcessSnapshot>($$$"""
        {"node_id":"{{{Node}}}","ts":"2026-04-20T12:00:10Z","tier":"raw","bucket_s":0,"by":"cpu","total":42,
         "prev":"2026-04-20T12:00:00Z","next":"2026-04-20T12:00:20Z",
         "processes":[{"key":"0a.Z28","pid":7,"ppid":1,"user":"root","name":"go","cmd":"go test","cpu_pct":12.5,"rss":2048,"threads":4,"start_ts":"2026-04-20T11:00:00Z"}]}
        """);

        s.Tier.Should().Be("raw");
        s.Total.Should().Be(42);
        s.Prev.Should().NotBeNull();
        s.Processes[0].Pid.Should().Be(7);
        s.Processes[0].Key.Should().Be("0a.Z28");
        s.Processes[0].CpuPctMax.Should().BeNull();
        s.Processes[0].Rss.Should().Be(2048UL);
    }

    [Fact]
    public void MinuteSnapshotHasNoPid()
    {
        var s = Decode<RemoteProcessSnapshot>($$$"""
        {"node_id":"{{{Node}}}","ts":"2026-04-20T12:00:00Z","tier":"1m","bucket_s":60,"by":"mem",
         "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","cpu_pct":10,"cpu_pct_max":88,"rss":2048}]}
        """);

        s.BucketS.Should().Be(60);
        s.Total.Should().BeNull();
        s.Processes[0].Pid.Should().BeNull();
        s.Processes[0].CpuPctMax.Should().Be(88);
    }

    [Fact]
    public void OffendersWithSeries()
    {
        var r = Decode<RemoteOffenders>($$$"""
        {"node_id":"{{{Node}}}","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z",
         "series_from":"2026-04-20T11:00:00Z","series_step_s":60,
         "processes":[{"key":"0a.Z28","name":"go","cmd":"go test","repo":"towertail","cpu_pct_avg":12.5,"cpu_pct_max":90,"rss_max":2048,
          "series":{"cpu":[1,2,3],"rss":[10,20,30],"io":[0,5,0]}},
          {"key":"0b.bm9kZQ","name":"node","cpu_pct_avg":1,"cpu_pct_max":2,"rss_max":1}]}
        """);

        r.SeriesStepS.Should().Be(60);
        r.Processes[0].Series!.Cpu.Should().Equal(1, 2, 3);
        r.Processes[0].Series!.Io.Should().Equal(0, 5, 0);
        r.Processes[0].Repo.Should().Be("towertail");
        r.Processes[1].Series.Should().BeNull();
    }

    [Fact]
    public void ProcessDetail()
    {
        var d = Decode<RemoteProcessDetail>($$$"""
        {"node_id":"{{{Node}}}","key":"0a.Z28","name":"go","cmd":"go test","repo":"towertail",
         "from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"10s",
         "points":[{"ts":"2026-04-20T11:59:50Z","cpu_pct":50,"rss":2048,"io_bps":1000}],
         "facts":{"seen_at":"2026-04-20T11:59:50Z","pid":7,"ppid":1,"parent":"zsh","user":"root","cwd":"/src","state":"R","threads":4,
                  "start_ts":"2026-04-20T11:00:00Z","listen_tcp":[8080],"listen_udp":[53]}}
        """);

        d.Points.Should().ContainSingle().Which.IoBps.Should().Be(1000);
        d.Facts!.Pid.Should().Be(7);
        d.Facts.ListenTcp.Should().Equal(8080);
        d.Facts.ListenUdp.Should().Equal(53);
    }

    [Fact]
    public void ProcessDetailWithNoFacts()
    {
        var d = Decode<RemoteProcessDetail>($$$"""
        {"node_id":"{{{Node}}}","key":"0a.Z28","name":"go","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"1m","points":[]}
        """);

        d.Facts.Should().BeNull();
        d.Cmd.Should().BeEmpty();
        d.Points.Should().BeEmpty();
    }

    [Fact]
    public void DiskInodes()
    {
        var r = Decode<RemoteDiskHistory>("""
        {"mounts":[{"mount":"/","fs":"ext4","points":[
          {"ts":"2026-04-20T12:00:00Z","used":50,"total":100,"inodes_used":9,"inodes_total":10},
          {"ts":"2026-04-20T12:01:00Z","used":50,"total":100}]}]}
        """);

        r.Mounts[0].Points[0].InodeRatio.Should().BeApproximately(0.9, 1e-9);
        r.Mounts[0].Points[0].Ratio.Should().Be(0.5);
        r.Mounts[0].Points[1].InodeRatio.Should().BeNull();
    }
}
