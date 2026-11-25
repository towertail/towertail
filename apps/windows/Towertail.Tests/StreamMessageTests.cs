using System.Text;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Xunit;
using static Towertail.WinUI.Backend.StreamMessage;

namespace Towertail.Tests;

public sealed class StreamMessageTests
{
    private static readonly Guid Id = Guid.Parse("11111111-2222-3333-4444-555555555555");

    private static StreamMessage? Decode(string json) => StreamMessage.Decode(Encoding.UTF8.GetBytes(json));

    private const string Summary = """{"ts":"2026-05-01T10:00:00Z","cpu":42,"mem":0.5,"disk":0.7,"rx":1,"tx":2,"load":0.5}""";

    [Fact]
    public void SampleFrame()
    {
        var sample = """{"v":1,"ts":"2026-05-01T10:00:00Z","host":{"name":"h","os":"linux","arch":"amd64","kernel":"6","uptime_s":1,"sampler":"s"},"cpu":{"pct":5,"load_1":0,"load_5":0,"load_15":0,"cores":2},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}""";
        var m = Decode($$$$"""{"type":"sample","nodeId":"{{{{Id}}}}","sample":{{{{sample}}}}}""");
        var f = m.Should().BeOfType<SampleFrame>().Subject;
        f.NodeId.Should().Be(Id);
        f.Sample.Cpu.Pct.Should().Be(5);
    }

    [Fact]
    public void NodeUpdatedFrameCarriesTheNodeId()
    {
        var m = Decode($$$$"""{"type":"node_updated","node":{"id":"{{{{Id}}}}","displayName":"db","kind":"cloud","enabled":true,"favorite":false}}""");
        var f = m.Should().BeOfType<NodeUpdatedFrame>().Subject;
        f.Node.Id.Should().Be(Id);
        f.Node.DisplayName.Should().Be("db");
    }

    [Fact]
    public void SettingsUpdatedFrame()
    {
        var m = Decode("""{"type":"settings_updated","settings":{"thresholds":{"cpuWarn":0.7,"cpuCritical":0.9,"memWarn":0.7,"memCritical":0.9,"diskWarn":0.8,"diskCritical":0.95},"localPollingIntervalSeconds":2,"sshPollingIntervalSeconds":10,"notificationsEnabled":true,"notifyWarn":true,"notifyCritical":true,"notifyDebounceSeconds":60,"autoUpdateSamplersEnabled":false,"postWakeGraceSeconds":15}}""");
        m.Should().BeOfType<SettingsUpdatedFrame>().Which.Settings.Thresholds.CpuWarn.Should().Be(0.7);
    }

    [Fact]
    public void DiskScanUpdatedFrame()
    {
        var job = Guid.NewGuid();
        var m = Decode($$$$"""{"type":"disk_scan_updated","nodeId":"{{{{Id}}}}","job":{"id":"{{{{job}}}}","node_id":"{{{{Id}}}}","state":"running","cancel_requested":false}}""");
        var f = m.Should().BeOfType<DiskScanUpdatedFrame>().Subject;
        f.NodeId.Should().Be(Id);
        f.Job.Id.Should().Be(job);
    }

    [Fact]
    public void EventFrame()
    {
        var m = Decode($$$$"""{"type":"event","event":{"nodeId":"{{{{Id}}}}","metric":"reachability","tint":"critical","kind":"reachability_changed"}}""");
        m.Should().BeOfType<EventFrame>().Which.Event.Should().Be(new StreamEvent(Id, "reachability", "critical", "reachability_changed"));
    }

    [Fact]
    public void EventFrameCarriesTheNotification()
    {
        var alert = Guid.Parse("aaaaaaaa-2222-3333-4444-555555555555");
        var m = Decode($$$$"""{"type":"event","event":{"id":"{{{{Guid.NewGuid()}}}}","alertId":"{{{{alert}}}}","nodeId":"{{{{Id}}}}","metric":"cpu","tint":"warn","kind":"threshold_crossed","notify":true,"title":"web: CPU warn","text":"CPU is at 91%."}}""");
        m.Should().BeOfType<EventFrame>().Which.Event.Should().Be(
            new StreamEvent(Id, "cpu", "warn", "threshold_crossed", alert, true, "web: CPU warn", "CPU is at 91%."));
    }

    [Fact]
    public void AlertUpdatedFrame()
    {
        var alert = Guid.Parse("aaaaaaaa-2222-3333-4444-555555555555");
        var m = Decode($$$$"""{"type":"alert_updated","nodeId":"{{{{Id}}}}","alert":{"id":"{{{{alert}}}}","node_id":"{{{{Id}}}}","kind":"threshold_crossed","metric":"cpu","tint":"critical","acked_by":"u1","acked_at":"2026-05-01T10:00:00Z"}}""");
        var a = m.Should().BeOfType<AlertUpdatedFrame>().Subject.Alert;
        a.Id.Should().Be(alert);
        a.NodeId.Should().Be(Id);
        a.AckedAt.Should().Be(new DateTimeOffset(2026, 5, 1, 10, 0, 0, TimeSpan.Zero));
        a.SnoozedUntil.Should().BeNull();
    }

    [Fact]
    public void HelloFrame()
    {
        var m = Decode("""{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}""");
        m.Should().BeOfType<HelloFrame>().Which.Hello.Should().Be(new StreamHello(true, 30, 120));
    }

    [Fact]
    public void SummaryFrame()
    {
        var m = Decode($$$$"""{"type":"summary","nodeId":"{{{{Id}}}}","summary":{{{{Summary}}}}}""");
        var f = m.Should().BeOfType<SummaryFrame>().Subject;
        f.NodeId.Should().Be(Id);
        f.Summary.Cpu.Should().Be(42);
    }

    [Fact]
    public void DetailFrame()
    {
        var m = Decode($$$$"""{"type":"detail","nodeId":"{{{{Id}}}}","tab":"health","detail":{"ts":"2026-05-01T10:00:00Z","health":{"procs":9}}}""");
        var f = m.Should().BeOfType<DetailFrame>().Subject;
        f.Tab.Should().Be(DetailTab.Health);
        f.Detail.Health!.Procs.Should().Be(9);
    }

    [Fact]
    public void LeaseFrame()
    {
        var m = Decode("""{"type":"lease","lease":{"state":"ended","reason":"limit"}}""");
        m.Should().BeOfType<LeaseFrame>().Which.Notice.IsLimit.Should().BeTrue();
    }

    [Fact]
    public void UnknownTypeGivesOtherFrame()
        => Decode("""{"type":"pong"}""").Should().Be(new OtherFrame("pong"));

    [Fact]
    public void BrokenBodyGivesOtherFrame()
        => Decode("""{"type":"summary","nodeId":"nope"}""").Should().BeOfType<OtherFrame>();

    [Fact]
    public void BadJsonGivesNull()
    {
        Decode("{not json").Should().BeNull();
        Decode("[]").Should().BeNull();
    }
}
