using FluentAssertions;
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
            var vm = new ServerViewModel(node);

            // Nominal → no fire.
            vm.Ingest(Make(node, 10, 10, 10));
            notifier.Evaluate(vm);
            count.Should().Be(0);

            // Warn → fire once.
            vm.Ingest(Make(node, 80, 10, 10));
            notifier.Evaluate(vm);
            count.Should().Be(1);

            // Still Warn, should not fire (same sev).
            vm.Ingest(Make(node, 82, 10, 10));
            notifier.Evaluate(vm);
            count.Should().Be(1);

            // Critical → fire.
            vm.Ingest(Make(node, 95, 10, 10));
            notifier.Evaluate(vm);
            count.Should().Be(2);
        }
        finally { File.Delete(path); }
    }

    private static Sample Make(Node n, double cpuPct, double memPct, double diskPct)
    {
        var memUsed = (long)(memPct / 100.0 * 16_000_000_000L);
        var diskUsed = (long)(diskPct / 100.0 * 500_000_000_000L);
        return new Sample(
            1, DateTime.UtcNow,
            new HostInfo(n.DisplayName, "windows", "amd64", "10.0", 0, "s", null),
            new CpuInfo(cpuPct, 0, 0, 0, 8),
            new MemInfo(memUsed, 16_000_000_000),
            new MemInfo(0, 0),
            new[] { new DiskSample("C:", "NTFS", diskUsed, 500_000_000_000) },
            null, null, null, null, Array.Empty<string>());
    }
}
