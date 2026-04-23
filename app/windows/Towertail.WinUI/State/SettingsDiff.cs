using Microsoft.Extensions.Logging;

namespace Towertail.WinUI.State;

/// <summary>
/// Log a field-level diff between two settings snapshots so the user can see exactly what
/// changed when they hit "Save" in Preferences. Same format as the Mac logSettingsDiff helper.
/// </summary>
public static class SettingsDiff
{
    public static void Log<T>(ILogger logger, T before, T after, IEnumerable<(string Name, Func<T, string> Extract)> fields)
    {
        foreach (var (name, extract) in fields)
        {
            var b = extract(before);
            var a = extract(after);
            if (b != a) logger.LogInformation("settings {Field}: {Before} -> {After}", name, b, a);
        }
    }
}
