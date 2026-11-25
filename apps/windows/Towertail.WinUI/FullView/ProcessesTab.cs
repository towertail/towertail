using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The Processes tab: the worst processes of the window, and the ports of the host.</summary>
public sealed class ProcessesTab : UserControl
{
    private static readonly double[] Widths = { 0, 90, 90, 100, 100, 130 };

    private readonly Segmented _mode = new(new[] { "Processes", "Ports" });
    private readonly Segmented _sortBar = new(Enum.GetValues<ProcSort>().Select(s => s.Title()));
    private readonly StackPanel _rows = new() { Spacing = 2, Padding = new Thickness(8, 0, 8, 8) };
    private readonly TextBlock _title = WB.Text("", 11, true, WB.Muted);
    private readonly PortsTable _ports = new();
    private readonly Grid _offenders = new();
    private ServerViewModel? _vm;
    private WorkbenchCursor? _cursor;
    private ProcSort _sort = ProcSort.Cpu;
    private DateTime _builtAt;
    private bool _portsBound;

    public event Action<bool>? PortsShown;

    public bool ShowsPorts => _mode.Selected == 1;

    public ProcessesTab()
    {
        var header = new Grid { Padding = new Thickness(16, 10, 16, 10), ColumnSpacing = 10 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_title, 1);
        Grid.SetColumn(_sortBar, 2);
        header.Children.Add(_mode);
        header.Children.Add(_title);
        header.Children.Add(_sortBar);

        var cols = Columns(Widths);
        string[] heads = { "Process", "Average", "Peak", "Max memory", "High CPU for", "Window" };
        for (var i = 0; i < heads.Length; i++)
        {
            var t = WB.Text(heads[i], 11, false, WB.Muted);
            Grid.SetColumn(t, i);
            cols.Children.Add(t);
        }
        cols.Margin = new Thickness(24, 8, 24, 8);

        var list = new Grid();
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var rule = new Border { Height = 1, Background = WB.Divider };
        Grid.SetRow(rule, 0);
        Grid.SetRow(cols, 1);
        var scroll = new ScrollViewer { Content = _rows, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
        Grid.SetRow(scroll, 2);
        list.Children.Add(rule);
        list.Children.Add(cols);
        list.Children.Add(scroll);

        var card = new Grid();
        card.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        card.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(list, 1);
        Grid.SetRow(_ports, 1);
        _ports.Visibility = Visibility.Collapsed;
        card.Children.Add(header);
        card.Children.Add(list);
        card.Children.Add(_ports);
        Content = WB.Card(card);

        _mode.Select(0);
        _sortBar.Select(0);
        _mode.SelectionChanged += i =>
        {
            list.Visibility = i == 0 ? Visibility.Visible : Visibility.Collapsed;
            _ports.Visibility = i == 1 ? Visibility.Visible : Visibility.Collapsed;
            _sortBar.Visibility = i == 0 ? Visibility.Visible : Visibility.Collapsed;
            if (i == 1 && !_portsBound && _vm is not null) { _ports.Bind(_vm); _portsBound = true; }
            PortsShown?.Invoke(i == 1);
        };
        _sortBar.SelectionChanged += i => { _sort = (ProcSort)i; _builtAt = DateTime.MinValue; Refresh(); };
    }

    private static Grid Columns(double[] widths)
    {
        var g = new Grid { ColumnSpacing = 8 };
        foreach (var w in widths)
            g.ColumnDefinitions.Add(new ColumnDefinition { Width = w == 0 ? new GridLength(1, GridUnitType.Star) : new GridLength(w) });
        return g;
    }

    public void Bind(ServerViewModel vm, WorkbenchCursor cursor)
    {
        _vm = vm;
        _cursor = cursor;
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName == nameof(ServerViewModel.LastSeen)) DispatcherQueue.TryEnqueue(Refresh);
        };
        cursor.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(WorkbenchCursor.Range) or nameof(WorkbenchCursor.Zoom))
                DispatcherQueue.TryEnqueue(() => { _builtAt = DateTime.MinValue; Refresh(); });
        };
        Loaded += (_, _) => { _builtAt = DateTime.MinValue; Refresh(); };
        Refresh();
    }

    private void Refresh()
    {
        if (_vm is null || _cursor is null || ShowsPorts) return;
        if (!IsLoaded) return;
        var now = DateTime.UtcNow;
        if (now - _builtAt < TimeSpan.FromSeconds(5)) return;
        _builtAt = now;
        var dark = WB.IsDark(this);
        var w = _cursor.Window(now);
        _title.Text = $"{w.From.ToLocalTime():HH:mm} → {w.To.ToLocalTime():HH:mm}";
        var top = new OffenderRanking(_vm.Procs.In(w), w, _sort).Top(50);
        _rows.Children.Clear();
        if (top.Count == 0)
        {
            _rows.Children.Add(WB.Text("No process data in this window.", 12, false, WB.Muted));
            return;
        }
        var maxAvg = Math.Max(100, top.Max(o => o.Avg));
        var green = WB.Brush(WB.Metric(RailMetric.Cpu, dark));
        foreach (var o in top)
        {
            var row = Columns(Widths);
            row.Padding = new Thickness(16, 6, 16, 6);
            var name = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
            name.Children.Add(WB.Text(o.Ref.Name, 13, false, null, true));
            var cmd = WB.Text(o.Ref.Cmd == o.Ref.Name ? "" : o.Ref.Cmd, 11, true, WB.Muted);
            cmd.TextTrimming = TextTrimming.CharacterEllipsis;
            name.Children.Add(cmd);
            var avg = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
            avg.Children.Add(new Border { Height = 4, Width = Math.Clamp(o.Avg / maxAvg, 0, 1) * 40, CornerRadius = new CornerRadius(2), Background = green, VerticalAlignment = VerticalAlignment.Center });
            avg.Children.Add(WB.Text(o.Avg.ToString("0.0"), 12, true));
            var cells = new UIElement[]
            {
                name, avg, WB.Text(o.Peak.ToString("0.0"), 12, true), WB.Text(StringFormatters.Bytes(o.RssMax), 12, true),
                WB.Text(o.HighFor.TotalSeconds < 1 ? "—" : ShortDuration.Text(o.HighFor), 12, true, WB.Muted), Spark(o, green),
            };
            for (var i = 0; i < cells.Length; i++)
            {
                Grid.SetColumn((FrameworkElement)cells[i], i);
                row.Children.Add(cells[i]);
            }
            var button = new Button
            {
                Content = row,
                HorizontalAlignment = HorizontalAlignment.Stretch,
                HorizontalContentAlignment = HorizontalAlignment.Stretch,
                Padding = new Thickness(0),
                BorderThickness = new Thickness(0),
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            };
            var r = o.Ref;
            button.Click += (_, _) => _cursor.OpenProcess = r;
            _rows.Children.Add(button);
        }
    }

    private static FrameworkElement Spark(Offender o, Brush stroke)
    {
        const double W = 120, H = 20;
        var max = Math.Max(1, o.Series.Max());
        var pts = new PointCollection();
        for (var i = 0; i < o.Series.Count; i++)
            pts.Add(new global::Windows.Foundation.Point(i / (double)Math.Max(1, o.Series.Count - 1) * W, H - 1 - o.Series[i] / max * (H - 2)));
        return new Polyline { Points = pts, Stroke = stroke, StrokeThickness = 1.2, Width = W, Height = H, VerticalAlignment = VerticalAlignment.Center };
    }
}
