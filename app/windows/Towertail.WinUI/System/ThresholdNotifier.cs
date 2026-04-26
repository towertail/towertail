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
    // Per-metric streak counters (warn / critical), reset on the first
    // under-threshold sample. In memory only — restarts start fresh.
    private readonly Dictionary<(Guid, Metric), int> _warnStreak = new();
    private readonly Dictionary<(Guid, Metric), int> _critStreak = new();
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
        if (vm.CpuPct is double cpu) Check(node, Metric.Cpu, cpu / 100.0, thresholds.CpuWarn, thresholds.CpuCritical, thresholds.CpuSustainSamples);
        if (vm.MemPct is double mem) Check(node, Metric.Mem, mem / 100.0, thresholds.MemWarn, thresholds.MemCritical, thresholds.MemSustainSamples);
        if (vm.DiskMaxPct is double disk) Check(node, Metric.Disk, disk / 100.0, thresholds.DiskWarn, thresholds.DiskCritical, thresholds.DiskSustainSamples);
    }

    private void Check(Node node, Metric m, double value, double warn, double critical, int sustain)
    {
        var key = (node.Id, m);
        // Update streaks before deriving the gated severity. A single sample
        // back under the warn line resets both counters so flapping clears
        // immediately.
        lock (_lock)
        {
            if (value >= warn) _warnStreak[key] = _warnStreak.GetValueOrDefault(key, 0) + 1;
            else _warnStreak[key] = 0;
            if (value >= critical) _critStreak[key] = _critStreak.GetValueOrDefault(key, 0) + 1;
            else _critStreak[key] = 0;
        }
        var need = Math.Max(1, sustain);
        var rawSev = value >= critical ? Severity.Critical
                   : value >= warn ? Severity.Warn
                   : Severity.Nominal;
        var sev = rawSev;
        if (need > 1)
        {
            int wStreak, cStreak;
            lock (_lock)
            {
                wStreak = _warnStreak.GetValueOrDefault(key, 0);
                cStreak = _critStreak.GetValueOrDefault(key, 0);
            }
            sev = rawSev switch
            {
                Severity.Critical => cStreak >= need ? Severity.Critical
                                  : wStreak >= need ? Severity.Warn
                                  : Severity.Nominal,
                Severity.Warn => wStreak >= need ? Severity.Warn : Severity.Nominal,
                _ => Severity.Nominal,
            };
        }
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
