using System.Globalization;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The Health tab: the score, its history, one row per check, the raw signals and the long-running processes.</summary>
public sealed class HealthTab : UserControl
{
    private readonly MetricChart _chart = new();
    private readonly Grid _ring = new() { Width = 120, Height = 120 };
    private readonly TextBlock _score = WB.Text("—", 32, false, null, true);
    private readonly TextBlock _level = WB.Text("", 12, true, WB.Muted);
    private readonly StackPanel _checks = new() { Spacing = 8, Padding = new Thickness(16, 4, 16, 12) };
    private readonly StackPanel _signals = new() { Spacing = 6, Padding = new Thickness(16, 4, 16, 12) };
    private readonly StackPanel _long = new() { Spacing = 6, Padding = new Thickness(16, 4, 16, 12) };
    private ServerViewModel? _vm;
    private WorkbenchCursor? _cursor;
    private WorkbenchData? _data;
    private bool _queued;
    private bool _dirty = true;

    public HealthTab()
    {
        var ringCard = new StackPanel { Spacing = 8, Padding = new Thickness(16), HorizontalAlignment = HorizontalAlignment.Center };
        var scoreBox = new StackPanel { VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center };
        _score.HorizontalAlignment = HorizontalAlignment.Center;
        _level.HorizontalAlignment = HorizontalAlignment.Center;
        scoreBox.Children.Add(_score);
        scoreBox.Children.Add(_level);
        var ringHost = new Grid { Width = 120, Height = 120 };
        ringHost.Children.Add(_ring);
        ringHost.Children.Add(scoreBox);
        ringCard.Children.Add(ringHost);

        var chartBox = new Grid { Margin = new Thickness(8, 8, 8, 8), MinHeight = 200 };
        chartBox.Children.Add(_chart);
        var left = new Grid { RowSpacing = 12 };
        left.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        left.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var topRow = new Grid { ColumnSpacing = 12 };
        topRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        topRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var chartCard = WB.Card(chartBox);
        Grid.SetColumn(chartCard, 1);
        topRow.Children.Add(WB.Card(ringCard));
        topRow.Children.Add(chartCard);
        left.Children.Add(topRow);

        var sections = new StackPanel { Spacing = 12 };
        sections.Children.Add(Section("Checks", _checks));
        sections.Children.Add(Section("Signals", _signals));
        sections.Children.Add(Section("Long-running processes", _long));
        var scroll = new ScrollViewer { Content = sections, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
        Grid.SetRow(scroll, 1);
        left.Children.Add(scroll);
        Content = left;
    }

    private static UIElement Section(string title, UIElement body)
    {
        var s = new StackPanel();
        var h = WB.Text(title.ToUpperInvariant(), 11, true, WB.Muted);
        h.CharacterSpacing = 80;
        h.Margin = new Thickness(16, 12, 16, 6);
        s.Children.Add(h);
        s.Children.Add(body);
        return WB.Card(s);
    }

    public void Bind(ServerViewModel vm, WorkbenchCursor cursor, WorkbenchData data)
    {
        _vm = vm;
        _cursor = cursor;
        _data = data;
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(ServerViewModel.LastSeen) or nameof(ServerViewModel.HealthScore) or nameof(ServerViewModel.Health)) Queue(true);
        };
        cursor.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(WorkbenchCursor.Range) or nameof(WorkbenchCursor.Zoom)) Queue(true);
        };
        data.HistoryLoaded += () => DispatcherQueue.TryEnqueue(() => Queue(true));
        Loaded += (_, _) => Refresh();
        Refresh();
    }

    private void Queue(bool data)
    {
        _dirty |= data;
        if (_queued) return;
        _queued = true;
        DispatcherQueue.TryEnqueue(() => { _queued = false; Refresh(); });
    }

    private void Refresh()
    {
        if (_vm is null || _cursor is null || _data is null) return;
        if (!IsLoaded) return;
        _dirty = false;
        _data.EnsureHistory(HostTab.Health);
        var dark = WB.IsDark(this);
        var hs = _vm.HealthScore;
        var tint = WB.Brush(WB.Level(hs.Level, dark));
        _score.Text = hs.Score.ToString("0");
        _score.Foreground = tint;
        _level.Text = hs.Level.ToString().ToUpperInvariant();
        DrawRing(hs.Score / 100, tint, dark);
        _chart.Bind(_data.Lines(RailMetric.Health, dark), _data.Window, _data.Format(RailMetric.Health), _cursor);

        _checks.Children.Clear();
        if (hs.Parts.Count == 0) _checks.Children.Add(WB.Text("No checks yet.", 12, false, WB.Muted));
        foreach (var p in hs.Parts) _checks.Children.Add(CheckRow(p, dark));

        RefreshSignals();
        RefreshLong();
    }

    private void DrawRing(double fraction, Brush tint, bool dark)
    {
        _ring.Children.Clear();
        _ring.Children.Add(new Ellipse { Stroke = WB.Stroke, StrokeThickness = 8, Width = 112, Height = 112 });
        fraction = Math.Clamp(fraction, 0.001, 0.9999);
        const double r = 56, cx = 60, cy = 60;
        var angle = fraction * 2 * Math.PI;
        var end = new global::Windows.Foundation.Point(cx + r * Math.Sin(angle), cy - r * Math.Cos(angle));
        var fig = new PathFigure { StartPoint = new global::Windows.Foundation.Point(cx, cy - r), IsClosed = false };
        fig.Segments.Add(new ArcSegment
        {
            Point = end,
            Size = new global::Windows.Foundation.Size(r, r),
            IsLargeArc = fraction > 0.5,
            SweepDirection = SweepDirection.Clockwise,
        });
        var geo = new PathGeometry();
        geo.Figures.Add(fig);
        var canvas = new Canvas { Width = 120, Height = 120 };
        canvas.Children.Add(new Microsoft.UI.Xaml.Shapes.Path { Data = geo, Stroke = tint, StrokeThickness = 8, StrokeStartLineCap = PenLineCap.Round, StrokeEndLineCap = PenLineCap.Round });
        _ring.Children.Add(canvas);
    }

    private static string ValueOf(HealthScorePart p) => p.Check switch
    {
        HealthCheck.Cpu or HealthCheck.Mem or HealthCheck.Disk or HealthCheck.Pids or HealthCheck.Files or HealthCheck.Inodes => (p.Value).ToString("P0", CultureInfo.InvariantCulture),
        HealthCheck.MemPressure => $"{p.Value:0}%",
        HealthCheck.IoPressure => $"{p.Value:0.0}%",
        _ => N((long)p.Value),
    };

    private static string LimitOf(HealthScorePart p)
    {
        if (p.Limit is not { } l) return "";
        return p.Check is HealthCheck.Procs or HealthCheck.Zombies or HealthCheck.MemPressure or HealthCheck.IoPressure
            ? $"warn {l.Warn:0.##} · critical {l.Critical:0.##}"
            : $"warn {l.Warn:P0} · critical {l.Critical:P0}";
    }

    private UIElement CheckRow(HealthScorePart p, bool dark)
    {
        var color = WB.Brush(WB.Level(p.Level, dark));
        var grid = new Grid { ColumnSpacing = 12 };
        foreach (var w in new[] { 180.0, 110.0, 0.0, 50.0 })
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = w == 0 ? new GridLength(1, GridUnitType.Star) : new GridLength(w) });
        var name = WB.Text(p.Label, 13);
        var value = WB.Text(ValueOf(p), 12, true, color, p.Level != HealthLevel.Nominal);
        var barBack = new Grid { Height = 6, VerticalAlignment = VerticalAlignment.Center };
        barBack.Children.Add(new Border { Background = WB.Stroke, CornerRadius = new CornerRadius(3) });
        var bar = new Border { Background = color, CornerRadius = new CornerRadius(3), HorizontalAlignment = HorizontalAlignment.Left };
        barBack.SizeChanged += (_, e) => bar.Width = e.NewSize.Width * Math.Clamp(p.Score / 100, 0, 1);
        barBack.Children.Add(bar);
        ToolTipService.SetToolTip(barBack, LimitOf(p));
        var score = WB.Text(p.Score.ToString("0"), 12, true, color);
        score.HorizontalAlignment = HorizontalAlignment.Right;
        Grid.SetColumn(value, 1);
        Grid.SetColumn(barBack, 2);
        Grid.SetColumn(score, 3);
        grid.Children.Add(name);
        grid.Children.Add(value);
        grid.Children.Add(barBack);
        grid.Children.Add(score);
        return grid;
    }

    private void RefreshSignals()
    {
        if (_vm is null) return;
        _signals.Children.Clear();
        var status = _vm.Health;
        var h = status.Info;
        if (h is null)
        {
            _signals.Children.Add(WB.Text("No health data from this host yet.", 12, false, WB.Muted));
            return;
        }
        Row("Processes", N(h.Procs), status.TintOf(HealthSignal.Procs));
        if (h.Zombies is int z)
        {
            var value = N(z);
            if (h.ZombieParents is { Count: > 0 } ps)
                value += "  —  " + string.Join(", ", ps.Select(p => $"{p.Name} (pid {p.Pid}): {N(p.Count)}"));
            Row("Zombies", value, status.TintOf(HealthSignal.Zombies));
        }
        if (h.PidsUsed is long pu && h.PidsMax is long pm) Row("PIDs", $"{N(pu)} / {N(pm)}", status.TintOf(HealthSignal.Pids));
        if (h.FilesUsed is long fu && h.FilesMax is long fm) Row("Open files", $"{N(fu)} / {N(fm)}", status.TintOf(HealthSignal.Files));
        if (h.Psi is { } psi)
        {
            Row("Memory pressure", $"some {psi.MemSome:0.0}%  ·  full {psi.MemFull:0.0}%", status.TintOf(HealthSignal.MemPressure));
            Row("I/O pressure", $"some {psi.IoSome:0.0}%  ·  full {psi.IoFull:0.0}%", status.TintOf(HealthSignal.IoPressure));
            Row("CPU pressure", $"some {psi.CpuSome:0.0}%", HealthLevel.Nominal);
        }
        if (h.MemPressure is int mp)
        {
            var label = mp switch { 4 => "critical", 2 => "warn", _ => "normal" };
            Row("Memory pressure", h.MemPressurePct is int pct ? $"{pct}% used  ·  kernel {label}" : $"{label} (update the sampler for a number)", status.TintOf(HealthSignal.MemPressure));
        }
        foreach (var d in status.Disks ?? Array.Empty<DiskSample>())
            if (d.InodesUsed is long iu && d.InodesTotal is long it && it > 0)
                Row($"Inodes {d.Mount}", $"{N(iu)} / {N(it)} ({(double)iu / it:P0})", HealthStatus.InodeLevel((double)iu / it, _vm.Thresholds.HealthOrDefault.Inodes));
    }

    private void Row(string label, string value, HealthLevel level)
    {
        var dark = WB.IsDark(this);
        var grid = new Grid { ColumnSpacing = 12 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(180) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(WB.Text(label, 13, false, WB.Muted));
        var v = WB.Text(value, 12, true, level == HealthLevel.Nominal ? null : WB.Brush(WB.Level(level, dark)), level != HealthLevel.Nominal);
        v.TextWrapping = TextWrapping.Wrap;
        Grid.SetColumn(v, 1);
        grid.Children.Add(v);
        _signals.Children.Add(grid);
    }

    private void RefreshLong()
    {
        if (_vm is null || _cursor is null) return;
        _long.Children.Clear();
        if (_vm.LongProcs.Count == 0)
        {
            _long.Children.Add(WB.Text("No long-running processes.", 12, false, WB.Muted));
            return;
        }
        var now = DateTime.UtcNow;
        foreach (var lp in _vm.LongProcs)
        {
            var row = new Grid { ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            var text = WB.Text($"{lp.ShortName}  pid {lp.Pid}  ·  {lp.CpuPct:0}% CPU  ·  high for {ShortDuration.Text(lp.HighFor(now))}", 12, true);
            var ignore = new Button { Content = "Ignore on this host", Padding = new Thickness(8, 2, 8, 2), FontSize = 11 };
            var name = lp.Name;
            ignore.Click += async (_, _) =>
            {
                var env = Towertail.WinUI.App.Current.Environment;
                await new LongProcIgnore(env.ServerSettings, env.Nodes, env.Servers, env.Backend).IgnoreAsync(name, _vm.Node.Id);
            };
            Grid.SetColumn(ignore, 1);
            row.Children.Add(text);
            row.Children.Add(ignore);
            _long.Children.Add(row);
        }
    }

    private static string N(long v) => v.ToString("N0", CultureInfo.InvariantCulture);
}
