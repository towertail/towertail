using CommunityToolkit.Mvvm.ComponentModel;

namespace Towertail.WinUI.State;

/// <summary>The six tabs of the host workbench.</summary>
public enum HostTab { Overview, Processes, Health, Disks, Alerts, Settings }

/// <summary>The metrics of the rail. The chart of the Overview tab shows the selected one.</summary>
public enum RailMetric { Cpu, Mem, Load, Disk, Net, Io, Health }

/// <summary>The sort of the process table. The names match the <c>by=</c> values of the server.</summary>
public enum ProcSort { Cpu, Rss, Io }

/// <summary>The window length of the charts.</summary>
public enum HostRange { H1, H6, H24, D7, D30 }

/// <summary>What the process snapshot of the workbench follows.</summary>
public enum CursorState { Live, Hover, Pinned }

public static class WorkbenchExtensions
{
    public static double Seconds(this HostRange r) => r switch
    {
        HostRange.H1 => 3_600,
        HostRange.H6 => 6 * 3_600,
        HostRange.H24 => 24 * 3_600,
        HostRange.D7 => 7 * 24 * 3_600,
        _ => 30 * 24 * 3_600,
    };

    /// <summary>The bucket of the history tier. About 300 points fill the window.</summary>
    public static TimeSpan Step(this HostRange r) => r switch
    {
        HostRange.H1 => TimeSpan.FromSeconds(10),
        HostRange.H6 => TimeSpan.FromSeconds(60),
        HostRange.H24 => TimeSpan.FromSeconds(300),
        HostRange.D7 => TimeSpan.FromSeconds(1_800),
        _ => TimeSpan.FromSeconds(7_200),
    };

    public static string Label(this HostRange r) => r switch
    {
        HostRange.H1 => "1h",
        HostRange.H6 => "6h",
        HostRange.H24 => "24h",
        HostRange.D7 => "7d",
        _ => "30d",
    };

    public static HostRange? Previous(this HostRange r) => r == HostRange.H1 ? null : r - 1;

    public static string Title(this HostTab t) => t.ToString().ToUpperInvariant();

    public static string Title(this RailMetric m) => m switch
    {
        RailMetric.Cpu => "CPU",
        RailMetric.Mem => "Memory",
        RailMetric.Load => "Load",
        RailMetric.Disk => "Disk",
        RailMetric.Net => "Network",
        RailMetric.Io => "Disk I/O",
        _ => "Health",
    };

    /// <summary>The process sort that fits the metric.</summary>
    public static ProcSort Sort(this RailMetric m) => m switch
    {
        RailMetric.Mem => ProcSort.Rss,
        RailMetric.Disk or RailMetric.Io => ProcSort.Io,
        _ => ProcSort.Cpu,
    };

    public static string Title(this ProcSort s) => s switch
    {
        ProcSort.Cpu => "CPU",
        ProcSort.Rss => "Memory",
        _ => "I/O",
    };

    /// <summary>Net and I/O draw two lines.</summary>
    public static bool HasTwoSeries(this RailMetric m) => m is RailMetric.Net or RailMetric.Io;
}

/// <summary>
/// The shared cursor of one workbench window: hover, pin, zoom, drag selection and range.
/// It lives as long as the window. The rules are the same as in the Mac <c>FullViewModel</c>.
/// </summary>
public sealed partial class WorkbenchCursor : ObservableObject
{
    /// <summary>Shorter drags do not zoom.</summary>
    public static readonly TimeSpan MinZoomSpan = TimeSpan.FromSeconds(120);

    [ObservableProperty] private DateTime? _hoverAt;
    [ObservableProperty] private DateTime? _pinnedAt;
    [ObservableProperty] private (DateTime From, DateTime To)? _zoom;
    [ObservableProperty] private (DateTime From, DateTime To)? _selection;
    [ObservableProperty] private ProcessRef? _openProcess;
    private HostRange _range = HostRange.H1;

    public HostRange Range
    {
        get => _range;
        set
        {
            if (_range == value) return;
            SetProperty(ref _range, value);
            Zoom = null;
            PinnedAt = null;
            HoverAt = null;
            Selection = null;
        }
    }

    public bool IsLive => PinnedAt is null;

    public CursorState State => HoverAt is not null ? CursorState.Hover : PinnedAt is not null ? CursorState.Pinned : CursorState.Live;

    /// <summary>The window of the charts: the zoom, or the range that ends now.</summary>
    public (DateTime From, DateTime To) Window(DateTime now)
        => Zoom ?? (now.AddSeconds(-Range.Seconds()), now);

    /// <summary>The bucket of the history for the window. About 300 points fill it.</summary>
    public TimeSpan HistoryStep(DateTime now)
    {
        if (Zoom is null) return Range.Step();
        var w = Window(now);
        return TimeSpan.FromSeconds(Math.Max(10, Math.Ceiling((w.To - w.From).TotalSeconds / 300)));
    }

    /// <summary>The time that the snapshot follows: hover, else the pin, else the newest.</summary>
    public DateTime? Effective(DateTime? latest) => HoverAt ?? PinnedAt ?? latest;

    /// <summary>The hover time, else the pin. Nil is live.</summary>
    public DateTime? Cursor => HoverAt ?? PinnedAt;

    /// <summary>The ordered drag selection, or null when there is no width.</summary>
    public (DateTime From, DateTime To)? PendingSelection
        => Selection is { } s && s.From != s.To ? (s.From < s.To ? s : (s.To, s.From)) : null;

    public void BeginSelection(DateTime t) => Selection = (t, t);

    public void UpdateSelection(DateTime t)
    {
        if (Selection is { } s) Selection = (s.From, t);
    }

    /// <summary>Ends a drag. A span of 120 s or more becomes the zoom window.</summary>
    public void EndSelection()
    {
        if (PendingSelection is { } r && r.To - r.From >= MinZoomSpan) Zoom = r;
        Selection = null;
    }

    public void ResetZoom()
    {
        Zoom = null;
        Selection = null;
    }

    /// <summary>Esc closes the process panel first, then the pin. False when it did nothing.</summary>
    public bool HandleEscape()
    {
        if (OpenProcess is not null)
        {
            OpenProcess = null;
            return true;
        }
        if (PinnedAt is null) return false;
        PinnedAt = null;
        return true;
    }

    /// <summary>
    /// Moves the pin by <c>direction * span</c>, inside the window and not after
    /// <paramref name="now"/>. False when the cursor is live.
    /// </summary>
    public bool StepPin(int direction, TimeSpan span, DateTime now)
    {
        if (PinnedAt is not { } t) return false;
        var w = Window(now);
        var upper = w.To < now ? w.To : now;
        var moved = t + direction * span;
        PinnedAt = moved < w.From ? w.From : moved > upper ? upper : moved;
        return true;
    }

    /// <summary>Space pins the newest time, or goes back to live when pinned.</summary>
    public void PinLatest(DateTime? latest)
    {
        if (PinnedAt is not null) PinnedAt = null;
        else if (latest is { } t) PinnedAt = t;
    }

    /// <summary>True when the range shows more than the smaller range before it. <paramref name="oldest"/> is the oldest time with data.</summary>
    public static bool IsAvailable(HostRange r, DateTime? oldest, DateTime now)
    {
        if (r.Previous() is not { } prev) return true;
        return oldest is { } o && o <= now.AddSeconds(-prev.Seconds());
    }
}
