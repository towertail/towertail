namespace Towertail.WinUI.State;

/// <summary>Short age texts. Mirrors ShortDuration.swift.</summary>
public static class ShortDuration
{
    /// <summary>"30s", "2m", "68h". Hours do not roll into days, the same as the card.</summary>
    public static string Text(TimeSpan d)
    {
        var s = (long)Math.Max(0, d.TotalSeconds);
        if (s < 60) return $"{s}s";
        if (s < 3600) return $"{s / 60}m";
        return $"{s / 3600}h";
    }

    /// <summary>"41m", "5h", "3d": the age of a process.</summary>
    public static string Age(TimeSpan d)
        => d.TotalSeconds < 86_400 ? Text(d) : $"{(long)d.TotalSeconds / 86_400}d";
}
