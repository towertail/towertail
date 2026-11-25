using Towertail.WinUI.State;
using Windows.UI;

namespace Towertail.WinUI.FullView;

/// <summary>One line of a chart.</summary>
public sealed record ChartSeries(string Name, Color Color, IReadOnlyList<TimedValue> Points);

/// <summary>How a chart writes its values. <see cref="Max"/> is null for an automatic top. Warn and critical draw as dashed lines.</summary>
public sealed record ChartFormat(Func<double, string> Value, double? Max = null, double? Warn = null, double? Critical = null);

public static class SeriesMath
{
    /// <summary>The value at or before <paramref name="t"/>; the first value when <paramref name="t"/> is older. Null when empty.</summary>
    public static double? ValueAt(IReadOnlyList<TimedValue> pts, DateTime t)
    {
        if (pts.Count == 0) return null;
        int lo = 0, hi = pts.Count - 1, best = 0;
        while (lo <= hi)
        {
            var mid = (lo + hi) / 2;
            if (pts[mid].T <= t) { best = mid; lo = mid + 1; } else hi = mid - 1;
        }
        return pts[best].Value;
    }

    public static IReadOnlyList<TimedValue> In(IReadOnlyList<TimedValue> pts, (DateTime From, DateTime To) w)
        => pts.Where(p => p.T >= w.From && p.T <= w.To).ToList();
}
