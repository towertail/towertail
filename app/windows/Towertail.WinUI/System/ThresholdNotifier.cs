using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Per-(host, metric) finite state machine (nominal ↔ warn ↔ critical) with debounce and
/// snooze/reachability gates. Mirrors ThresholdNotifier.swift.
/// </summary>
public sealed class ThresholdNotifier
{
    public enum Severity { Nominal, Warn, Critical }
    public enum Metric { Cpu, Mem, Disk }

    private readonly ServerSettings _settings;
    private readonly NodeStore _nodes;
    private readonly Dictionary<(Guid, Metric), Severity> _state = new();
    private readonly Dictionary<(Guid, Metric), DateTime> _lastNotified = new();
    private readonly object _lock = new();

    public event EventHandler<NotifyPayload>? Raised;

    public ThresholdNotifier(ServerSettings settings, NodeStore nodes)
    {
        _settings = settings;
        _nodes = nodes;
    }

    public void Evaluate(ServerViewModel vm)
    {
        var node = vm.Node;
        var thresholds = node.CustomThresholds ?? _settings.Thresholds;
        if (vm.CpuPct is double cpu) Check(node, Metric.Cpu, cpu / 100.0, thresholds.CpuWarn, thresholds.CpuCritical);
        if (vm.MemPct is double mem) Check(node, Metric.Mem, mem / 100.0, thresholds.MemWarn, thresholds.MemCritical);
        if (vm.DiskMaxPct is double disk) Check(node, Metric.Disk, disk / 100.0, thresholds.DiskWarn, thresholds.DiskCritical);
    }

    private void Check(Node node, Metric m, double value, double warn, double critical)
    {
        var sev = value >= critical ? Severity.Critical
                : value >= warn ? Severity.Warn
                : Severity.Nominal;
        bool changed;
        Severity prior;
        lock (_lock)
        {
            prior = _state.GetValueOrDefault((node.Id, m), Severity.Nominal);
            changed = prior != sev;
            _state[(node.Id, m)] = sev;
        }
        if (!changed) return;
        if (sev == Severity.Nominal) return;

        if (node.IsSnoozed) return;
        if (!_settings.NotificationsEnabled) return;
        if (sev == Severity.Warn && !(_settings.NotifyWarn && node.NotifyOnWarn)) return;
        if (sev == Severity.Critical && !(_settings.NotifyCritical && node.NotifyOnCritical)) return;

        lock (_lock)
        {
            var last = _lastNotified.GetValueOrDefault((node.Id, m), DateTime.MinValue);
            if ((DateTime.UtcNow - last).TotalSeconds < _settings.NotifyDebounceSeconds) return;
            _lastNotified[(node.Id, m)] = DateTime.UtcNow;
        }

        Raised?.Invoke(this, new NotifyPayload(node.Id, m, sev, value));
    }

    public sealed record NotifyPayload(Guid NodeId, Metric Metric, Severity Severity, double Value);
}
