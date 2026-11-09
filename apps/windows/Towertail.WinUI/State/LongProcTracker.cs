namespace Towertail.WinUI.State;

/// <summary>Identity of one process across samples. A reused PID with a new start time is a new key.</summary>
public readonly record struct LongProcKey(int Pid, long? StartTicks, string? Name)
{
    public static LongProcKey Of(ProcSample p)
        => new(p.Pid, p.StartTs?.ToUniversalTime().Ticks, p.StartTs is null ? p.Name : null);
}

/// <summary>A process that stayed over the CPU limit for the sustain time.</summary>
public sealed record LongProc(LongProcKey Key, int Pid, string Name, string? Cmd, double CpuPct, DateTime HighSince, DateTime? StartTs)
{
    /// <summary>Name cut at 24 characters, the same as the notification labels.</summary>
    public string ShortName
    {
        get
        {
            var n = string.IsNullOrEmpty(Name) ? $"pid {Pid}" : Name;
            return n.Length > 24 ? n[..23] + "…" : n;
        }
    }

    public TimeSpan HighFor(DateTime now) => now - HighSince;
}

/// <summary>
/// Follows each process of one host over time. It stores only level changes, so a 24 h
/// window stays small. Mirrors LongProcTracker.swift and the server tracker.
/// </summary>
public sealed class LongProcTracker
{
    private readonly record struct Point(DateTime T, bool High);

    private sealed class Entry
    {
        public required ProcSample Proc;
        public readonly List<Point> Points = new();
        public DateTime LastAt;
        public DateTime? HighSince;
        public bool Flagged;
        /// <summary>False when the latest sample did not list the key. Such a key keeps its history but is not reported.</summary>
        public bool Present = true;
    }

    private readonly Dictionary<LongProcKey, Entry> _entries = new();

    public int TrackedCount => _entries.Count;
    public int ChangePointCount => _entries.Values.Sum(e => e.Points.Count);

    public void Reset() => _entries.Clear();

    /// <summary>Feeds one sample. <c>items == null</c> or <paramref name="skipped"/> records nothing, so the last level holds.</summary>
    public IReadOnlyList<LongProc> Observe(IReadOnlyList<ProcSample>? items, bool skipped, DateTime at, LongProcRule rule, IReadOnlySet<string> ignored)
    {
        if (!rule.Enabled)
        {
            _entries.Clear();
            return Array.Empty<LongProc>();
        }
        if (items is null || skipped) return Flagged();
        if (_entries.Count > 0 && at < _entries.Values.Max(e => e.LastAt)) _entries.Clear();

        var seen = new HashSet<LongProcKey>();
        foreach (var p in items)
        {
            if (ignored.Contains(p.Name.ToLowerInvariant())) continue;
            var key = LongProcKey.Of(p);
            if (!seen.Add(key)) continue;
            var high = p.CpuPct >= rule.CpuPct;
            if (_entries.TryGetValue(key, out var e))
            {
                e.Proc = p;
                e.Present = true;
                Record(e, high, at, rule);
            }
            else if (high)
            {
                e = new Entry { Proc = p, LastAt = at };
                Record(e, true, at, rule);
                _entries[key] = e;
            }
        }
        foreach (var (key, e) in _entries.ToList())
        {
            if (seen.Contains(key)) continue;
            if (ignored.Contains(e.Proc.Name.ToLowerInvariant()))
            {
                _entries.Remove(key);
                continue;
            }
            e.Present = false;
            Record(e, false, at, rule);
        }
        foreach (var (key, e) in _entries.ToList())
            if (!e.Points.Exists(pt => pt.High)) _entries.Remove(key);
        return Flagged();
    }

    public IReadOnlyList<LongProc> Flagged()
        => _entries
            .Where(kv => kv.Value is { Flagged: true, Present: true, HighSince: not null })
            .Select(kv => new LongProc(kv.Key, kv.Value.Proc.Pid, kv.Value.Proc.Name, kv.Value.Proc.Cmd,
                kv.Value.Proc.CpuPct, kv.Value.HighSince!.Value, kv.Value.Proc.StartTs))
            .OrderByDescending(p => p.CpuPct)
            .ToList();

    private static void Record(Entry e, bool high, DateTime t, LongProcRule rule)
    {
        if (e.Points.Count == 0 || e.Points[^1].High != high) e.Points.Add(new Point(t, high));
        e.LastAt = t;
        if (high && e.HighSince is null) e.HighSince = t;
        var start = t - TimeSpan.FromSeconds(rule.SustainSeconds);
        while (e.Points.Count > 1 && e.Points[1].T <= start) e.Points.RemoveAt(0);
        var full = e.Points.Count > 0 && e.Points[0].T <= start;
        e.Flagged = full && HighFraction(e.Points, start, t) >= LongProcRule.Tolerance;
        // The run ends when its own high fraction falls below the tolerance.
        if (!e.Flagged && e.HighSince is DateTime since)
        {
            var from = since > start ? since : start;
            if (t > from && HighFraction(e.Points, from, t) < LongProcRule.Tolerance)
                e.HighSince = high ? t : null;
        }
    }

    /// <summary>Time-weighted high fraction over start..now. Each point holds its level until the next point.</summary>
    private static double HighFraction(List<Point> points, DateTime start, DateTime now)
    {
        var span = (now - start).TotalSeconds;
        if (span <= 0) return 0;
        double high = 0;
        for (var i = 0; i < points.Count; i++)
        {
            if (!points[i].High) continue;
            var end = i + 1 < points.Count ? points[i + 1].T : now;
            var from = points[i].T > start ? points[i].T : start;
            var d = (end - from).TotalSeconds;
            if (d > 0) high += d;
        }
        return high / span;
    }
}
