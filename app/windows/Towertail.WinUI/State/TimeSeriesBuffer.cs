namespace Towertail.WinUI.State;

/// <summary>
/// Bounded append-only buffer of (timestamp, value) points — the core of MetricSeries.
/// Decimation + decay happens at the owning series level; this struct just stores.
/// </summary>
public sealed class TimeSeriesBuffer
{
    private readonly List<TimedValue> _points;
    private readonly int _capacity;

    public TimeSeriesBuffer(int capacity = 4096)
    {
        _capacity = capacity;
        _points = new List<TimedValue>(Math.Min(capacity, 512));
    }

    public IReadOnlyList<TimedValue> Points => _points;
    public int Count => _points.Count;

    public void Append(DateTime t, double value)
    {
        _points.Add(new TimedValue(t, value));
        if (_points.Count > _capacity)
            _points.RemoveRange(0, _points.Count - _capacity);
    }

    public void TrimOlderThan(DateTime cutoff)
    {
        var firstKeep = 0;
        while (firstKeep < _points.Count && _points[firstKeep].T < cutoff) firstKeep++;
        if (firstKeep > 0) _points.RemoveRange(0, firstKeep);
    }

    public void Clear() => _points.Clear();
}

public readonly record struct TimedValue(DateTime T, double Value);
