using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class ThresholdNotifierTests
{
    [Fact]
    public void FiresOnceOnWarnToCriticalTransition()
    {
        var path = Path.GetTempFileName();
        try
        {
            var nodes = new NodeStore(path);
            var settings = new ServerSettings(path);
            settings.NotificationsEnabled = true;
            settings.NotifyWarn = true;
            settings.NotifyCritical = true;
            settings.NotifyDebounceSeconds = 0;
            var notifier = new ThresholdNotifier(settings, nodes);
            int count = 0;
            notifier.Raised += (_, _) => Interlocked.Increment(ref count);

            var node = new Node { DisplayName = "n", Kind = NodeKind.Local };
            nodes.Add(node);
            var vm = new ServerViewModel(node) { AlertRules = AlertRulesTests.Immediate };

            // Nominal → no fire.
            vm.Ingest(SampleFixture.Make(cpu: 10));
            notifier.Evaluate(vm);
            count.Should().Be(0);

            // Warn → fire once.
            vm.Ingest(SampleFixture.Make(cpu: 80));
            notifier.Evaluate(vm);
            count.Should().Be(1);

            // Still Warn, should not fire (same sev).
            vm.Ingest(SampleFixture.Make(cpu: 82));
            notifier.Evaluate(vm);
            count.Should().Be(1);

            // Critical → fire.
            vm.Ingest(SampleFixture.Make(cpu: 95));
            notifier.Evaluate(vm);
            count.Should().Be(2);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ForwardSendsMacStyleToast()
    {
        var path = Path.GetTempFileName();
        try
        {
            var nodes = new NodeStore(path);
            var settings = new ServerSettings(path)
            {
                NotificationsEnabled = true, NotifyWarn = true, NotifyCritical = true, NotifyDebounceSeconds = 0,
            };
            var notifier = new ThresholdNotifier(settings, nodes);
            var sink = new FakeSink();
            notifier.Forward(sink);
            var node = new Node { DisplayName = "web1", Kind = NodeKind.Local };
            nodes.Add(node);
            var vm = new ServerViewModel(node) { AlertRules = AlertRulesTests.Immediate };

            vm.Ingest(SampleFixture.Make(cpu: 95));
            notifier.Evaluate(vm);

            sink.Calls.Should().ContainSingle().Which.Should().Be(("web1: CPU Critical", "CPU at 95%", node.Id, "cpu"));
        }
        finally { File.Delete(path); }
    }

    [Theory]
    [InlineData(ThresholdNotifier.Metric.Mem, ThresholdNotifier.Severity.Warn, 0.8, null, "web1: MEM Warning", "Memory at 80%", "mem")]
    [InlineData(ThresholdNotifier.Metric.Disk, ThresholdNotifier.Severity.Critical, 0.93, null, "web1: DISK Critical", "Disk at 93%", "disk")]
    [InlineData(ThresholdNotifier.Metric.Health, ThresholdNotifier.Severity.Warn, 1.0, "x", "web1: HEALTH Warning", "x", "health")]
    [InlineData(ThresholdNotifier.Metric.Health, ThresholdNotifier.Severity.Warn, 1.0, "", "web1: HEALTH Warning", "Threshold crossed", "health")]
    [InlineData(ThresholdNotifier.Metric.NoData, ThresholdNotifier.Severity.Critical, 180.0, "No sample for 3m.", "web1: no data", "No sample for 3m.", "cpu")]
    [InlineData(ThresholdNotifier.Metric.LongProc, ThresholdNotifier.Severity.Warn, 85.0, "p", "web1: long-running process", "p", "cpu")]
    public void PayloadTextPerMetric(
        ThresholdNotifier.Metric metric, ThresholdNotifier.Severity severity, double value, string? body,
        string title, string text, string tab)
    {
        var p = new ThresholdNotifier.NotifyPayload(Guid.NewGuid(), metric, severity, value, body);
        p.Title("web1").Should().Be(title);
        p.Text.Should().Be(text);
        p.Tab.Should().Be(tab);
    }

    private sealed class FakeSink : INotificationSink
    {
        public List<(string Title, string Body, Guid HostId, string Metric)> Calls { get; } = new();
        public void Notify(string title, string body, Guid hostId, string metric) => Calls.Add((title, body, hostId, metric));
    }
}
