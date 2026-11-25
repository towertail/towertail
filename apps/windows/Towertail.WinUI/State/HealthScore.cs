namespace Towertail.WinUI.State;

/// <summary>The checks that make the score.</summary>
public enum HealthCheck { Cpu, Mem, Disk, Procs, Zombies, Pids, Files, MemPressure, IoPressure, Inodes, LongProc }

/// <summary>One check of the score. <see cref="Limit"/> is null for a check with no limits.</summary>
public sealed record HealthScorePart(HealthCheck Check, double Value, double Score, HealthLevel Level, ThresholdPair? Limit)
{
    public string Label => Check switch
    {
        HealthCheck.Cpu => "CPU",
        HealthCheck.Mem => "Memory",
        HealthCheck.Disk => "Disk",
        HealthCheck.Procs => ThresholdMetric.Procs.Title(),
        HealthCheck.Zombies => ThresholdMetric.Zombies.Title(),
        HealthCheck.Pids => ThresholdMetric.Pids.Title(),
        HealthCheck.Files => ThresholdMetric.Files.Title(),
        HealthCheck.MemPressure => ThresholdMetric.MemPressure.Title(),
        HealthCheck.IoPressure => ThresholdMetric.IoPressure.Title(),
        HealthCheck.Inodes => ThresholdMetric.Inodes.Title(),
        _ => "Long-running process",
    };
}

/// <summary>
/// Host health as one number. The score is the worst check. Bands: 100 to 81 below warn,
/// 80 to 41 from warn to critical, 40 to 0 at critical or above. Same as the Mac <c>HealthScore</c>.
/// </summary>
public sealed record HealthScore
{
    public const double LongProcScore = 70;

    /// <summary>Worst first.</summary>
    public IReadOnlyList<HealthScorePart> Parts { get; }

    public HealthScore(IReadOnlyList<HealthScorePart> parts)
        => Parts = parts.OrderBy(p => p.Score).ToList();

    public static readonly HealthScore Empty = new(Array.Empty<HealthScorePart>());

    public double Score => Parts.Count == 0 ? 100 : Parts[0].Score;
    public HealthScorePart? Worst => Parts.Count == 0 ? null : Parts[0];
    public HealthLevel Level => LevelOf(Score);

    /// <summary>
    /// <paramref name="cpu"/>, <paramref name="mem"/> and <paramref name="disk"/> are ratios.
    /// <paramref name="inodeRatio"/> counts only when <paramref name="disks"/> is null.
    /// Muted checks are skipped.
    /// </summary>
    public static HealthScore Evaluate(HealthInfo? info, IReadOnlyList<DiskSample>? disks, double? cpu, double? mem, double? disk,
        IReadOnlyList<LongProc> longProcs, MetricThresholds t, MutedSignals muted,
        double? inodeRatio = null, bool kernelCriticalOnly = false)
    {
        var parts = new List<HealthScorePart>();
        void Add(HealthCheck check, double value, ThresholdPair? pair, double? score = null)
        {
            var s = score ?? (pair is null ? 100 : ScoreOf(value, pair.Warn, pair.Critical));
            parts.Add(new HealthScorePart(check, value, s, LevelOf(s), pair));
        }
        void Signal(HealthSignal sig, HealthCheck check, double value, ThresholdPair pair, double? score = null)
        {
            if (!muted.Mutes(sig)) Add(check, value, pair, score);
        }

        if (cpu is double c && !muted.Mutes(AlertMetric.Cpu)) Add(HealthCheck.Cpu, c, t.Get(ThresholdMetric.Cpu));
        if (mem is double m && !muted.Mutes(AlertMetric.Mem)) Add(HealthCheck.Mem, m, t.Get(ThresholdMetric.Mem));
        if (disk is double d && !muted.Mutes(AlertMetric.Disk)) Add(HealthCheck.Disk, d, t.Get(ThresholdMetric.Disk));

        if (info is { } h)
        {
            var limits = t.HealthOrDefault;
            Signal(HealthSignal.Procs, HealthCheck.Procs, h.Procs, t.Get(ThresholdMetric.Procs));
            if (h.Zombies is int z) Signal(HealthSignal.Zombies, HealthCheck.Zombies, z, t.Get(ThresholdMetric.Zombies));
            if (Frac(h.PidsUsed, h.PidsMax) is double pf) Signal(HealthSignal.Pids, HealthCheck.Pids, pf, limits.Pids);
            if (Frac(h.FilesUsed, h.FilesMax) is double ff) Signal(HealthSignal.Files, HealthCheck.Files, ff, limits.Files);
            if (h.MemPressurePct is int mp)
                Signal(HealthSignal.MemPressure, HealthCheck.MemPressure, mp, limits.MacMemPressure);
            else if (h.Psi is { } psi)
            {
                var pair = limits.MemPressure;
                Signal(HealthSignal.MemPressure, HealthCheck.MemPressure, psi.MemSome, pair, PsiScore(psi.MemSome, psi.MemFull, pair));
            }
            else if (h.MemPressure is int level)
            {
                // A sampler older than 1.3.0 sends only the kernel level.
                var s = level == 4 ? 20 : level == 2 && !kernelCriticalOnly ? 60 : 100;
                Signal(HealthSignal.MemPressure, HealthCheck.MemPressure, level, limits.MemPressure, s);
            }
            if (h.Psi is { } io) Signal(HealthSignal.IoPressure, HealthCheck.IoPressure, io.IoFull, limits.IoPressure);
        }

        double? inode = disks is null ? inodeRatio : null;
        foreach (var x in disks ?? Array.Empty<DiskSample>())
            if (Frac(x.InodesUsed, x.InodesTotal) is double f && f > (inode ?? -1)) inode = f;
        if (inode is double i) Signal(HealthSignal.Inodes, HealthCheck.Inodes, i, t.HealthOrDefault.Inodes);

        if (longProcs.Count > 0 && !muted.Contains(AlertSignal.Long))
            Add(HealthCheck.LongProc, longProcs.Count, null, LongProcScore);
        return new HealthScore(parts);
    }

    public static double ScoreOf(double value, double warn, double critical)
    {
        if (value >= critical) return Math.Max(0, 40 - 40 * (value - critical) / Math.Max(critical, double.Epsilon));
        if (value >= warn) return 80 - 39 * (value - warn) / Math.Max(critical - warn, double.Epsilon);
        return 100 - 19 * (value / Math.Max(warn, double.Epsilon));
    }

    public static HealthLevel LevelOf(double score)
        => score >= 81 ? HealthLevel.Nominal : score >= 41 ? HealthLevel.Warn : HealthLevel.Critical;

    /// <summary>Linux memory pressure goes critical on "full" and warns on "some".</summary>
    private static double PsiScore(double some, double full, ThresholdPair pair)
    {
        if (full >= pair.Critical) return ScoreOf(full, pair.Critical, pair.Critical);
        var s = ScoreOf(some, pair.Warn, pair.Critical);
        return some >= pair.Warn ? Math.Max(41, s) : s;
    }

    private static double? Frac(long? used, long? max)
        => used is long u && max is long m && m > 0 ? (double)u / m : null;
}
