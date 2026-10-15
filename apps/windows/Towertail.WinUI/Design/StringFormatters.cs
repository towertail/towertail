namespace Towertail.WinUI.Design;

public static class StringFormatters
{
    public static string Bytes(long bytes)
    {
        if (bytes < 1024) return $"{bytes} B";
        double kb = bytes / 1024.0;
        if (kb < 1024) return $"{kb:0.#} KB";
        double mb = kb / 1024.0;
        if (mb < 1024) return $"{mb:0.#} MB";
        double gb = mb / 1024.0;
        if (gb < 1024) return $"{gb:0.##} GB";
        return $"{gb / 1024.0:0.##} TB";
    }

    public static string RelativeTime(DateTime? t)
    {
        if (t is null) return "—";
        var elapsed = DateTime.UtcNow - t.Value;
        if (elapsed.TotalSeconds < 5) return "now";
        if (elapsed.TotalSeconds < 60) return $"{(int)elapsed.TotalSeconds}s ago";
        if (elapsed.TotalMinutes < 60) return $"{(int)elapsed.TotalMinutes}m ago";
        if (elapsed.TotalHours < 24) return $"{(int)elapsed.TotalHours}h ago";
        return $"{(int)elapsed.TotalDays}d ago";
    }
}
