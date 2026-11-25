using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The Disks tab: capacity and I/O of the host, and the usage scan.</summary>
public sealed class DisksTab : UserControl
{
    private readonly Segmented _mode = new(new[] { "Capacity & I/O", "Usage" });
    private readonly ContentControl _slot = new() { HorizontalContentAlignment = HorizontalAlignment.Stretch, VerticalContentAlignment = VerticalAlignment.Stretch };
    private readonly StackPanel _mounts = new() { Spacing = 8, Padding = new Thickness(16, 4, 16, 12) };
    private readonly MetricChart _capacity = new();
    private readonly MetricChart _io = new();
    private readonly Grid _overview = new() { RowSpacing = 12 };
    private DiskUsageView? _usage;
    private ServerViewModel? _vm;
    private WorkbenchCursor? _cursor;
    private WorkbenchData? _data;
    private bool _queued;

    public event Action<bool>? UsageShown;

    public bool ShowsUsage => _mode.Selected == 1;

    public DisksTab()
    {
        var root = new Grid { RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        _mode.HorizontalAlignment = HorizontalAlignment.Left;
        Grid.SetRow(_slot, 1);
        root.Children.Add(_mode);
        root.Children.Add(_slot);
        Content = root;

        _overview.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        _overview.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var mountCard = WB.Card(new ScrollViewer { Content = _mounts, MaxHeight = 200, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        var charts = new Grid { ColumnSpacing = 12 };
        charts.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        charts.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var capCard = ChartCard("DISK USE", _capacity);
        var ioCard = ChartCard("DISK I/O", _io);
        Grid.SetColumn(ioCard, 1);
        charts.Children.Add(capCard);
        charts.Children.Add(ioCard);
        Grid.SetRow(charts, 1);
        _overview.Children.Add(mountCard);
        _overview.Children.Add(charts);

        _mode.Select(0);
        _slot.Content = _overview;
        _mode.SelectionChanged += i =>
        {
            _slot.Content = i == 1 ? _usage ??= (_vm is null ? null : new DiskUsageView(_vm, Towertail.WinUI.App.Current.Environment)) : _overview;
            UsageShown?.Invoke(i == 1);
        };
    }

    private static Border ChartCard(string title, MetricChart chart)
    {
        var g = new Grid { RowSpacing = 4, Padding = new Thickness(8, 10, 8, 8) };
        g.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        g.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var t = WB.Text(title, 11, true, WB.Muted);
        t.Margin = new Thickness(8, 0, 0, 0);
        Grid.SetRow(chart, 1);
        g.Children.Add(t);
        g.Children.Add(chart);
        return WB.Card(g);
    }

    public void Bind(ServerViewModel vm, WorkbenchCursor cursor, WorkbenchData data)
    {
        _vm = vm;
        _cursor = cursor;
        _data = data;
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName == nameof(ServerViewModel.LastSeen)) Queue();
        };
        cursor.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(WorkbenchCursor.Range) or nameof(WorkbenchCursor.Zoom)) Queue();
        };
        data.HistoryLoaded += () => DispatcherQueue.TryEnqueue(Queue);
        Loaded += (_, _) => Refresh();
        Refresh();
    }

    public void Stop() => _usage?.Stop();

    private void Queue()
    {
        if (_queued) return;
        _queued = true;
        DispatcherQueue.TryEnqueue(() => { _queued = false; Refresh(); });
    }

    private void Refresh()
    {
        if (_vm is null || _cursor is null || _data is null) return;
        if (!IsLoaded) return;
        _data.EnsureHistory(HostTab.Disks);
        var dark = WB.IsDark(this);
        _capacity.Bind(_data.Lines(RailMetric.Disk, dark), _data.Window, _data.Format(RailMetric.Disk), _cursor);
        _io.Bind(_data.Lines(RailMetric.Io, dark), _data.Window, _data.Format(RailMetric.Io), _cursor);

        _mounts.Children.Clear();
        if (_vm.Disk.Mounts.Count == 0) _mounts.Children.Add(WB.Text("No disk data yet.", 12, false, WB.Muted));
        foreach (var m in _vm.Disk.Mounts.ToArray())
        {
            var t = _vm.Thresholds.Get(ThresholdMetric.Disk);
            var level = m.Frac >= t.Critical ? HealthLevel.Critical : m.Frac >= t.Warn ? HealthLevel.Warn : HealthLevel.Nominal;
            var color = WB.Brush(level == HealthLevel.Nominal ? WB.Metric(RailMetric.Disk, dark) : WB.Level(level, dark));
            var row = new Grid { ColumnSpacing = 12 };
            foreach (var w in new[] { 160.0, 80.0, 0.0, 260.0 })
                row.ColumnDefinitions.Add(new ColumnDefinition { Width = w == 0 ? new GridLength(1, GridUnitType.Star) : new GridLength(w) });
            var back = new Grid { Height = 6, VerticalAlignment = VerticalAlignment.Center };
            back.Children.Add(new Border { Background = WB.Stroke, CornerRadius = new CornerRadius(3) });
            var bar = new Border { Background = color, CornerRadius = new CornerRadius(3), HorizontalAlignment = HorizontalAlignment.Left };
            var frac = Math.Clamp(m.Frac, 0, 1);
            back.SizeChanged += (_, e) => bar.Width = e.NewSize.Width * frac;
            back.Children.Add(bar);
            var fs = WB.Text(m.Fs, 11, true, WB.Muted);
            var inodes = _data.Cloud?.InodeRatio(m.Mount) is double ir ? $" · inodes {ir:P0}" : "";
            var amount = WB.Text($"{StringFormatters.Bytes(m.Used)} of {StringFormatters.Bytes(m.Total)} · {m.Frac:P0}{inodes}", 12, true, level == HealthLevel.Nominal ? null : color);
            Grid.SetColumn(fs, 1);
            Grid.SetColumn(back, 2);
            Grid.SetColumn(amount, 3);
            row.Children.Add(WB.Text(m.Mount, 13, false, null, true));
            row.Children.Add(fs);
            row.Children.Add(back);
            row.Children.Add(amount);
            _mounts.Children.Add(row);
        }
    }
}
