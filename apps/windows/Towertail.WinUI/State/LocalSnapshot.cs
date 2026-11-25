namespace Towertail.WinUI.State;

/// <summary>The process list of a local or SSH host at one time, sorted.</summary>
public sealed record LocalSnapshot(IReadOnlyList<ProcSample> Rows, DateTime? T, int Total)
{
    public static readonly LocalSnapshot Empty = new(Array.Empty<ProcSample>(), null, 0);

    /// <summary>The snapshot nearest before <paramref name="at"/>, or the newest when <paramref name="at"/> is null.</summary>
    public static LocalSnapshot From(ProcSeries series, DateTime? at, ProcSort sort, int limit = 60)
    {
        var snap = (at is { } t ? series.At(t) : null) ?? series.Latest;
        if (snap is null) return Empty;
        IEnumerable<ProcSample> all = sort switch
        {
            ProcSort.Rss => snap.Items.OrderByDescending(p => p.Rss),
            ProcSort.Io => snap.Items.OrderByDescending(p => p.ReadBps is null && p.WriteBps is null ? -1 : (p.ReadBps ?? 0) + (p.WriteBps ?? 0))
                .ThenByDescending(p => p.CpuPct),
            _ => snap.Items.OrderByDescending(p => p.CpuPct),
        };
        return new LocalSnapshot(all.Take(limit).ToList(), snap.T, snap.Items.Count);
    }

    /// <summary>"14:02:11 · now" or "14:02:11 · 3m ago".</summary>
    public string Label(DateTime now)
    {
        if (T is not { } t) return "no snapshot";
        var age = now - t;
        var when = age.TotalSeconds < 15 ? "now" : ShortDuration.Text(age) + " ago";
        return t.ToLocalTime().ToString("HH:mm:ss") + " · " + when;
    }
}
