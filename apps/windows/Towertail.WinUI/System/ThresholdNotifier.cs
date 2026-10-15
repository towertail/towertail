using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Per-(host, metric) finite state machine over the sustain-gated alert level, with per-rule
/// notify gates, debounce, and snooze. Mirrors ThresholdNotifier.swift.
/// </summary>
public sealed class ThresholdNotifier
{
    public enum Severity { Nominal, Warn, Critical }
    public enum Metric { Cpu, Mem, Disk, Health }

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

    /// <summary>
    /// Fires on escalation of the sustain-gated alert level. A downgrade only updates
    /// state, so the next escalation can fire again.
    /// </summary>
    public void Evaluate(ServerViewModel vm)
    {
        var node = vm.Node;
        if (vm.CpuPct is double cpu) Check(node, vm, Metric.Cpu, AlertMetric.Cpu, cpu / 100.0);
        if (vm.MemPct is double mem) Check(node, vm, Metric.Mem, AlertMetric.Mem, mem / 100.0);
        if (vm.DiskMaxPct is double disk) Check(node, vm, Metric.Disk, AlertMetric.Disk, disk / 100.0);
        Check(node, vm, Metric.Health, AlertMetric.Health, (double)vm.Health.Level, vm.Health.Body);
    }

    private void Check(Node node, ServerViewModel vm, Metric m, AlertMetric am, double value, string? body = null)
    {
        var level = vm.AlertLevel(am);
        var sev = level switch
        {
            HealthLevel.Critical => Severity.Critical,
            HealthLevel.Warn => Severity.Warn,
            _ => Severity.Nominal,
        };
        Severity prior;
        lock (_lock)
        {
            prior = _state.GetValueOrDefault((node.Id, m), Severity.Nominal);
            _state[(node.Id, m)] = sev;
        }
        if (sev <= prior) return;

        if (!vm.AlertRules.For(am).Allows(level)) return;
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

        Raised?.Invoke(this, new NotifyPayload(node.Id, m, sev, value, body));
    }

    public sealed record NotifyPayload(Guid NodeId, Metric Metric, Severity Severity, double Value, string? Body = null);
}
