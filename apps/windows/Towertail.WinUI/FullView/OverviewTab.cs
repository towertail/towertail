using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The Overview tab: the metric rail, the chart of the selected metric and the top processes.</summary>
public sealed class OverviewTab : UserControl
{
    private readonly MetricRail _rail = new();
    private readonly MetricChart _chart = new();
    private readonly ProcessTable _table = new();
    private readonly TextBlock _stats = WB.Text("", 12, true, WB.Muted);
    private readonly TextBlock _big = WB.Text("", 28, false, null, true);
    private readonly TextBlock _big2 = WB.Text("", 28, false, null, true);
    private readonly TextBlock _bigAt = WB.Text("now", 12, true, WB.Muted);
    private ServerViewModel? _vm;
    private WorkbenchCursor? _cursor;
    private WorkbenchData? _data;
    private RailMetric _metric = RailMetric.Cpu;
    private bool _queued;
    private bool _dirtyData = true;

    public OverviewTab()
    {
        var statsRow = new Grid { Padding = new Thickness(16, 12, 16, 0), ColumnSpacing = 12 };
        statsRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        statsRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        _stats.VerticalAlignment = VerticalAlignment.Top;
        _stats.Margin = new Thickness(0, 6, 0, 0);
        var big = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        big.Children.Add(_big);
        big.Children.Add(_big2);
        var bigAt = _bigAt;
        bigAt.VerticalAlignment = VerticalAlignment.Bottom;
        bigAt.Margin = new Thickness(0, 0, 0, 6);
        big.Children.Add(bigAt);
        Grid.SetColumn(big, 1);
        statsRow.Children.Add(_stats);
        statsRow.Children.Add(big);

        var chartHost = new Grid { Margin = new Thickness(8, 0, 8, 0) };
        chartHost.Children.Add(_chart);
        var hint = WB.Text("hover to inspect · click to pin · drag to zoom", 11, true, WB.Muted);
        hint.Margin = new Thickness(16, 0, 16, 8);

        var chartGrid = new Grid();
        chartGrid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        chartGrid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        chartGrid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Grid.SetRow(chartHost, 1);
        Grid.SetRow(hint, 2);
        chartGrid.Children.Add(statsRow);
        chartGrid.Children.Add(chartHost);
        chartGrid.Children.Add(hint);
        var chartCard = WB.Card(chartGrid);

        var right = new Grid { RowSpacing = 12 };
        right.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1.2, GridUnitType.Star), MinHeight = 240 });
        right.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 200 });
        Grid.SetRow(_table, 1);
        right.Children.Add(chartCard);
        right.Children.Add(_table);

        var root = new Grid { ColumnSpacing = 12 };
        root.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        root.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Grid.SetColumn(right, 1);
        root.Children.Add(_rail);
        root.Children.Add(right);
        Content = root;

        _rail.Selected += m => Rail = m;
    }

    public RailMetric Rail
    {
        get => _metric;
        set
        {
            _metric = value;
            _rail.Select(value);
            _table.Sort = value.Sort();
            Queue(true);
        }
    }

    public void Bind(ServerViewModel vm, WorkbenchCursor cursor, WorkbenchData data)
    {
        _vm = vm;
        _cursor = cursor;
        _data = data;
        _table.Bind(vm, cursor);
        _rail.Select(_metric);
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(ServerViewModel.LastSeen) or nameof(ServerViewModel.HealthScore)) Queue(true);
        };
        cursor.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(WorkbenchCursor.Selection) or nameof(WorkbenchCursor.OpenProcess)) return;
            Queue(a.PropertyName is nameof(WorkbenchCursor.Zoom) or nameof(WorkbenchCursor.Range));
        };
        data.HistoryLoaded += () => DispatcherQueue.TryEnqueue(() => Queue(true));
        Loaded += (_, _) => { _dirtyData = true; Refresh(); };
        Refresh();
    }

    private void Queue(bool data)
    {
        _dirtyData |= data;
        if (_queued) return;
        _queued = true;
        DispatcherQueue.TryEnqueue(() => { _queued = false; Refresh(); });
    }

    private void Refresh()
    {
        if (_vm is null || _cursor is null || _data is null) return;
        if (!IsLoaded) return;
        var dirty = _dirtyData;
        _dirtyData = false;
        if (dirty) _data.EnsureHistory();
        var dark = WB.IsDark(this);
        var now = DateTime.UtcNow;
        var window = _data.Window;
        var at = _cursor.Cursor;

        _rail.Update(_data.Tiles(at, dark));

        var lines = _data.Lines(_metric, dark);
        var format = _data.Format(_metric);
        if (dirty) _chart.Bind(lines, window, format, _cursor);

        var first = lines.Count > 0 ? lines[0].Points.Select(p => p.Value).ToList() : new List<double>();
        var parts = new List<string> { _metric.Title().ToUpperInvariant() };
        if (first.Count > 0)
        {
            parts.Add($"avg {format.Value(first.Average())}");
            parts.Add($"peak {format.Value(first.Max())}");
        }
        if (format.Warn is double w) parts.Add($"warn {format.Value(w)}");
        if (format.Critical is double c) parts.Add($"critical {format.Value(c)}");
        _stats.Text = string.Join(" · ", parts);

        var t = at ?? now;
        _big.Text = _data.ValueText(_metric, lines, t);
        var v = lines.Count > 0 ? SeriesMath.ValueAt(lines[0].Points, t) : null;
        _big.Foreground = _data.Tint(_metric, v, dark) is { } tint ? WB.Brush(tint) : WB.Fg;
        if (_metric.HasTwoSeries())
        {
            _big.Foreground = WB.Brush(lines[0].Color);
            _big2.Text = _data.ValueText(_metric, lines, t, 1);
            _big2.Foreground = WB.Brush(lines[1].Color);
        }
        else _big2.Text = "";
        _bigAt.Text = _cursor.HoverAt is { } h ? "at " + h.ToLocalTime().ToString("HH:mm:ss")
            : _cursor.PinnedAt is { } p ? "pinned " + p.ToLocalTime().ToString("HH:mm:ss") : "now";
    }
}
