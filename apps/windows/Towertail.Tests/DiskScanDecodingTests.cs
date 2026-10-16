using System.Text.Json;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class DiskScanDecodingTests
{
    [Theory]
    [InlineData("linux")]
    [InlineData("darwin")]
    [InlineData("windows")]
    public void DecodesGoldenFixture(string os)
    {
        var events = ScanFixture.Lines(os).Select(DiskScanCodec.DecodeLine).ToList();

        events[..^1].Should().AllBeOfType<DiskScanEvent.Progress>();
        var result = events[^1].Should().BeOfType<DiskScanEvent.Result>().Subject.Value;
        result.Os.Should().Be(os);
        result.Hostname.Should().Be($"dev-{os}");
        result.Tree.Children.Should().NotBeEmpty();
        result.Tree.B.Should().BeGreaterThan(0);
        result.Recommendations.Should().NotBeEmpty();
        result.Recommendations.Should().OnlyContain(r => r.Group == DiskRec.SafeGroup || r.Group == DiskRec.ReviewGroup);
        result.Fs.Should().NotBeNull();
        result.FinishedAt.Should().Be(new DateTime(2026, 10, 1, 12, 0, 12, 340, DateTimeKind.Utc));

        var cmds = result.Recommendations.SelectMany(r => os == "windows" ? r.Cmd.Pwsh! : r.Cmd.Sh!);
        cmds.Should().NotBeEmpty();
    }

    [Fact]
    public void ProgressLineHasCounters()
    {
        var p = DiskScanCodec.DecodeLine(ScanFixture.Lines("linux")[0]).Should().BeOfType<DiskScanEvent.Progress>().Subject.Line;
        p.Files.Should().Be(9);
        p.UsedBytes.Should().Be(31_000_000_000);
        p.Current.Should().Be("/tmp");
    }

    [Fact]
    public void ResultSurvivesEncodeRoundTrip()
    {
        var result = ScanFixture.Result("windows");
        var again = DiskScanCodec.DecodeResult(DiskScanCodec.Encode(result));
        again.Hostname.Should().Be(result.Hostname);
        again.Recommendations.Select(r => r.Id).Should().Equal(result.Recommendations.Select(r => r.Id));
        again.Tree.Children.Count.Should().Be(result.Tree.Children.Count);
    }

    [Fact]
    public void DecodesErrorAndCancelledLines()
    {
        DiskScanCodec.DecodeLine("""{"t":"error","v":1,"message":"root not found"}""")
            .Should().Be(new DiskScanEvent.Error("root not found"));
        DiskScanCodec.DecodeLine("""{"t":"cancelled","v":1}""").Should().BeOfType<DiskScanEvent.Cancelled>();
    }

    [Theory]
    [InlineData("""{"t":"progress"}""")]
    [InlineData("""{"t":"progress","v":2}""")]
    [InlineData("""{"t":"mystery","v":1}""")]
    [InlineData("not json")]
    public void RejectsBadLines(string line)
    {
        var act = () => DiskScanCodec.DecodeLine(line);
        act.Should().Throw<JsonException>();
    }

    [Fact]
    public void RequestArgumentsLeaveOutNulls()
    {
        new DiskScanRequest().Arguments().Should().Equal("scan");
        new DiskScanRequest("~", 4, 1000).Arguments()
            .Should().Equal("scan", "--root", "~", "--max-depth", "4", "--min-bytes", "1000");
    }

    [Theory]
    [InlineData(0, "0 B")]
    [InlineData(999, "999 B")]
    [InlineData(1_500, "2 KB")]
    [InlineData(12_400_000, "12 MB")]
    [InlineData(1_840_000_000, "1.8 GB")]
    [InlineData(2_500_000_000_000, "2.5 TB")]
    public void FormatsSizes(long bytes, string text) => DiskSize.Format(bytes).Should().Be(text);

    [Fact]
    public void DecodesServerJob()
    {
        var job = JsonSerializer.Deserialize<DiskScanJob>("""
            {"id":"6f2c1a4e-0000-0000-0000-000000000001","node_id":"6f2c1a4e-0000-0000-0000-000000000002",
             "state":"running","root":"/","cancel_requested":false,"created_ts":"2026-10-01T12:00:00Z",
             "progress":{"t":"progress","v":1,"files":5,"dirs":1,"bytes":10,"no_access_dirs":0,"current":"/x","elapsed_ms":5,"used_bytes":100}}
            """, DiskScanCodec.Options)!;
        job.State.Should().Be(DiskScanJob.Running);
        job.IsFinal.Should().BeFalse();
        job.Progress!.Files.Should().Be(5);
    }
}
