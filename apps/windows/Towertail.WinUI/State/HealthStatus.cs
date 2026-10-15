using System.Globalization;

namespace Towertail.WinUI.State;

public enum HealthLevel { Nominal, Warn, Critical }

public enum HealthSignal { Procs, Zombies, Pids, Files, MemPressure, IoPressure, Inodes }

public sealed record HealthReason(HealthSignal Signal, HealthLevel Level, string Text);

/// <summary>
/// Host health verdict for one sample: the worst level across all rules plus one reason per
/// rule that tripped, critical first. Mirrors the Mac HealthStatus. Process and zombie limits
/// come from <see cref="MetricThresholds"/>; the other limits are fixed.
/// </summary>
public sealed record HealthStatus(
    HealthLevel Level,
    IReadOnlyList<HealthReason> Reasons,
    HealthInfo? Info,
    IReadOnlyList<DiskSample>? Disks = null)
{
    public const double PidsWarn = 0.70, PidsCritical = 0.90;
    public const double FilesWarn = 0.80, FilesCritical = 0.95;
    public const double InodesWarn = 0.85, InodesCritical = 0.95;
    public const double MemPsiWarn = 10, MemPsiCritical = 20;
    public const double IoPsiWarn = 20, IoPsiCritical = 40;

    public static readonly HealthStatus Nominal = new(HealthLevel.Nominal, Array.Empty<HealthReason>(), null);

    /// <summary>First reason plus "+N more", or null when nominal.</summary>
    public string? Summary => Reasons.Count switch
    {
        0 => null,
        1 => Reasons[0].Text,
        _ => $"{Reasons[0].Text} +{Reasons.Count - 1} more",
    };

    public string Body => string.Join(" · ", Reasons.Select(r => r.Text));

    /// <summary>Worst level among the reasons for <paramref name="signal"/>.</summary>
    public HealthLevel TintOf(HealthSignal signal)
        => Reasons.Where(r => r.Signal == signal).Select(r => r.Level).DefaultIfEmpty(HealthLevel.Nominal).Max();

    /// <summary>Worst level among the reasons not from <paramref name="signal"/>.</summary>
    public HealthLevel LevelExcluding(HealthSignal signal)
        => Reasons.Where(r => r.Signal != signal).Select(r => r.Level).DefaultIfEmpty(HealthLevel.Nominal).Max();

    public static HealthStatus Evaluate(HealthInfo? h, IReadOnlyList<DiskSample>? disks, MetricThresholds t)
    {
        var reasons = new List<HealthReason>();

        if (h is not null)
        {
            Add(reasons, HealthSignal.Procs, Count(h.Procs, t.ProcsWarn, t.ProcsCritical), $"{N(h.Procs)} processes");

            if (h.Zombies is int z)
            {
                var top = h.ZombieParents is { Count: > 0 } ps ? $" ({ps[0].Name})" : "";
                Add(reasons, HealthSignal.Zombies, Count(z, t.ZombiesWarn, t.ZombiesCritical), $"{N(z)} zombies{top}");
            }

            if (Frac(h.PidsUsed, h.PidsMax) is double pf)
                Add(reasons, HealthSignal.Pids, Ratio(pf, PidsWarn, PidsCritical), $"PIDs {Pct(pf)} of limit");

            if (Frac(h.FilesUsed, h.FilesMax) is double ff)
                Add(reasons, HealthSignal.Files, Ratio(ff, FilesWarn, FilesCritical), $"Open files {Pct(ff)} of limit");

            if (h.Psi is { } psi)
            {
                if (psi.MemFull >= MemPsiCritical)
                    Add(reasons, HealthSignal.MemPressure, HealthLevel.Critical, $"Memory pressure {psi.MemFull:0}%");
                else if (psi.MemSome >= MemPsiWarn)
                    Add(reasons, HealthSignal.MemPressure, HealthLevel.Warn, $"Memory pressure {psi.MemSome:0}%");
                Add(reasons, HealthSignal.IoPressure, Ratio(psi.IoFull, IoPsiWarn, IoPsiCritical), $"I/O pressure {psi.IoFull:0}%");
            }

            if (h.MemPressure is 4)
                Add(reasons, HealthSignal.MemPressure, HealthLevel.Critical, "Memory pressure critical");
            else if (h.MemPressure is 2)
                Add(reasons, HealthSignal.MemPressure, HealthLevel.Warn, "Memory pressure high");
        }

        if (disks is not null)
        {
            foreach (var d in disks)
            {
                if (Frac(d.InodesUsed, d.InodesTotal) is double f)
                    Add(reasons, HealthSignal.Inodes, InodeLevel(f), $"Inodes {Pct(f)} on {d.Mount}");
            }
        }

        // Stable sort: critical first, rule order kept within a level.
        var ordered = reasons.OrderByDescending(r => r.Level).ToList();
        var level = ordered.Count == 0 ? HealthLevel.Nominal : ordered[0].Level;
        return new HealthStatus(level, ordered, h, disks);
    }

    /// <summary>Level for one disk's inode use (fraction 0–1).</summary>
    public static HealthLevel InodeLevel(double frac) => Ratio(frac, InodesWarn, InodesCritical);

    private static void Add(List<HealthReason> list, HealthSignal signal, HealthLevel level, string text)
    {
        if (level != HealthLevel.Nominal) list.Add(new HealthReason(signal, level, text));
    }

    private static HealthLevel Count(int v, int warn, int critical)
        => v >= critical ? HealthLevel.Critical : v >= warn ? HealthLevel.Warn : HealthLevel.Nominal;

    private static HealthLevel Ratio(double v, double warn, double critical)
        => v >= critical ? HealthLevel.Critical : v >= warn ? HealthLevel.Warn : HealthLevel.Nominal;

    private static double? Frac(long? used, long? max)
        => used is long u && max is long m && m > 0 ? (double)u / m : null;

    private static string N(int v) => v.ToString("N0", CultureInfo.InvariantCulture);
    private static string Pct(double f) => $"{Math.Round(f * 100):0}%";
}
