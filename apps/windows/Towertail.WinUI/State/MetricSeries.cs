using System.Collections.ObjectModel;

namespace Towertail.WinUI.State;

/// <summary>
/// Rolling window of per-metric samples (CPU%, MEM%, DISK%, NET Mbps). Owns a decimation pass
/// that merges adjacent points outside a "fresh" window so the sparkline stays dense near
/// now but falls off exponentially over the 2h retention horizon.
/// </summary>
public sealed class MetricSeries
{
    public static readonly TimeSpan Retention = TimeSpan.FromHours(2);
    /// <summary>Window of raw, undecimated points near "now" (kept dense for sparkline fidelity).</summary>
    public static readonly TimeSpan FreshWindow = TimeSpan.FromSeconds(120);

    public ObservableCollection<TimedValue> Points { get; } = new();

    private readonly int _cap;
    public MetricSeries(int capacity = 4096) { _cap = capacity; }

    public void Append(DateTime t, double value)
    {
        Points.Add(new TimedValue(t, value));
        EnforceRetention();
    }

    public void Replace(IEnumerable<TimedValue> points)
    {
        Points.Clear();
        foreach (var p in points) Points.Add(p);
        EnforceRetention();
    }

    public double? Latest => Points.Count == 0 ? null : Points[^1].Value;

    private void EnforceRetention()
    {
        var cutoff = DateTime.UtcNow - Retention;
        while (Points.Count > 0 && Points[0].T < cutoff) Points.RemoveAt(0);
        while (Points.Count > _cap) Points.RemoveAt(0);
    }
}
