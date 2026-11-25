namespace Towertail.WinUI.State;

/// <summary>One process group in the offenders table.</summary>
public sealed record Offender(ProcessRef Ref, double Avg, double Peak, long RssMax, TimeSpan HighFor, IReadOnlyList<double> Series)
{
    public string Id => Ref.Key;
}

/// <summary>
/// Ranks the processes of a window. Groups the PIDs of one command, averages over all
/// snapshots of the window and keeps <c>points</c> values for the sparkline.
/// </summary>
public sealed class OffenderRanking
{
    public const double HighCpu = 50;

    private sealed class Acc
    {
        public required ProcessRef Ref;
        public double Sum, Peak, IoSum;
        public long RssMax;
        public double HighSeconds;
        public readonly Dictionary<int, double> PerBucket = new();
    }

    private readonly IReadOnlyList<Offender> _offenders;

    public OffenderRanking(IEnumerable<ProcSeries.Snapshot> snapshots, (DateTime From, DateTime To) window, ProcSort by, int points = 60)
    {
        var snaps = snapshots.Where(s => s.T >= window.From && s.T <= window.To).OrderBy(s => s.T).ToList();
        if (snaps.Count == 0 || points <= 0)
        {
            _offenders = Array.Empty<Offender>();
            return;
        }
        var step = (window.To - window.From).TotalSeconds / points;
        var groups = new Dictionary<string, Acc>();
        for (var i = 0; i < snaps.Count; i++)
        {
            var snap = snaps[i];
            var dt = i + 1 < snaps.Count ? (snaps[i + 1].T - snap.T).TotalSeconds
                : i > 0 ? (snap.T - snaps[i - 1].T).TotalSeconds : 0;
            var bucket = step > 0 ? Math.Min(points - 1, (int)((snap.T - window.From).TotalSeconds / step)) : 0;
            var perGroup = new Dictionary<string, (double Cpu, long Rss, double Io, ProcessRef Ref)>();
            foreach (var p in snap.Items)
            {
                var r = ProcessRef.Local(p);
                perGroup.TryGetValue(r.Key, out var g);
                perGroup[r.Key] = (g.Cpu + p.CpuPct, g.Rss + p.Rss,
                    g.Io + Math.Max(p.ReadBps ?? 0, 0) + Math.Max(p.WriteBps ?? 0, 0), g.Ref ?? r);
            }
            foreach (var (key, g) in perGroup)
            {
                if (!groups.TryGetValue(key, out var acc)) groups[key] = acc = new Acc { Ref = g.Ref };
                acc.Sum += g.Cpu;
                acc.IoSum += g.Io;
                acc.Peak = Math.Max(acc.Peak, g.Cpu);
                acc.RssMax = Math.Max(acc.RssMax, g.Rss);
                if (g.Cpu >= HighCpu) acc.HighSeconds += dt;
                acc.PerBucket[bucket] = Math.Max(acc.PerBucket.GetValueOrDefault(bucket), g.Cpu);
            }
        }
        double n = snaps.Count;
        _offenders = groups.Values
            .Select(a =>
            {
                var avg = a.Sum / n;
                var key = by switch { ProcSort.Cpu => avg, ProcSort.Rss => (double)a.RssMax, _ => a.IoSum / n };
                var series = Enumerable.Range(0, points).Select(b => a.PerBucket.GetValueOrDefault(b)).ToList();
                return (Offender: new Offender(a.Ref, avg, a.Peak, a.RssMax, TimeSpan.FromSeconds(a.HighSeconds), series), Key: key);
            })
            .OrderByDescending(x => x.Key)
            .ThenBy(x => x.Offender.Ref.Key, StringComparer.Ordinal)
            .Select(x => x.Offender)
            .ToList();
    }

    public IReadOnlyList<Offender> Top(int n) => _offenders.Take(n).ToList();
}
