using System.Globalization;

namespace Towertail.WinUI.State;

/// <summary>Texts of alert timing and notify levels. The cloud rules views and the move sheet use them.</summary>
public static class AlertText
{
    /// <summary>"At once", "30 s", "5 min", "1 h". The same as the Mac sustain picker.</summary>
    public static string Sustain(int seconds) => seconds switch
    {
        0 => "At once",
        < 60 => $"{seconds} s",
        3600 => "1 h",
        _ when seconds % 60 == 0 => $"{seconds / 60} min",
        _ => $"{seconds / 60} min {seconds % 60} s",
    };

    public static string Notify(AlertNotify n) => n switch
    {
        AlertNotify.Off => "Off",
        AlertNotify.Critical => "Critical",
        _ => "Warn + Critical",
    };

    /// <summary>"5,000" for a count, "75" for a fraction in percent, without the percent sign.</summary>
    public static string Number(ThresholdMetric m, double v)
    {
        var n = (long)Math.Round(v * m.Scale(), MidpointRounding.AwayFromZero);
        return m.IsCount() ? n.ToString("N0", CultureInfo.InvariantCulture) : n.ToString(CultureInfo.InvariantCulture);
    }

    /// <summary>"75 / 90".</summary>
    public static string Pair(ThresholdMetric m, ThresholdPair p) => $"{Number(m, p.Warn)} / {Number(m, p.Critical)}";

    public static string Percent(double fraction)
        => $"{(long)Math.Round(fraction * 100, MidpointRounding.AwayFromZero)}%";

    /// <summary>"60% · 10 min", or "off".</summary>
    public static string LongProc(LongProcRule r) => r.Enabled ? $"{(int)r.CpuPct}% · {Sustain(r.SustainSeconds)}" : "off";
}
