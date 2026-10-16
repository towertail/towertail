using System.ComponentModel;
using Microsoft.UI;
using Microsoft.UI.Dispatching;
using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;
using Windows.ApplicationModel.DataTransfer;
using Windows.UI;

namespace Towertail.WinUI.FullView;

/// <summary>
/// The Usage sub-tab of DISK: scope picker, stat tiles, scan progress, a two-level treemap,
/// the recommendations and the cleanup script. Towertail only shows and copies the script.
/// </summary>
public sealed class DiskUsageView : UserControl
{
    private static readonly Color Green = Color.FromArgb(0xFF, 0x34, 0xC7, 0x59);
    private static readonly Color NoAccessGray = Color.FromArgb(0xFF, 0x8C, 0x8C, 0x94);

    private static readonly (string Cat, Color Color, string Label)[] Categories =
    {
        ("code", Color.FromArgb(0xFF, 124, 111, 230), "Code projects"),
        ("ai", Color.FromArgb(0xFF, 214, 112, 166), "AI models"),
        ("cache", Color.FromArgb(0xFF, 91, 143, 209), "Package caches"),
        ("sdk", Color.FromArgb(0xFF, 79, 179, 184), "SDKs & toolchains"),
        ("docs", Color.FromArgb(0xFF, 208, 138, 75), "Documents & media"),
        ("other", Color.FromArgb(0xFF, 140, 140, 148), "System & other"),
    };

    private readonly ServerViewModel _vm;
    private readonly DiskUsageModel _model;
    private readonly DispatcherQueueTimer _clock;
    private readonly List<DiskNode> _focusPath = new();

    private readonly ComboBox _scope = new() { MinWidth = 160 };
    private readonly TextBlock _status = new() { VerticalAlignment = VerticalAlignment.Center, Opacity = 0.7 };
    private readonly Button _rescan = new() { Content = "Rescan" };
    private readonly Button _cancel = new() { Content = "Cancel scan" };
    private readonly StackPanel _jobStrip = new() { Orientation = Orientation.Horizontal, Spacing = 10 };
    private readonly Border _jobCard;
    private readonly InfoBar _error = new() { Severity = InfoBarSeverity.Error, IsClosable = false };
    private readonly InfoBar _stale = new() { Severity = InfoBarSeverity.Warning, IsClosable = false };
    private readonly Grid _tiles = new() { ColumnSpacing = 12 };
    private readonly StackPanel _progress = new() { Spacing = 8 };
    private readonly Border _progressCard;
    private readonly StackPanel _crumbs = new() { Orientation = Orientation.Horizontal, Spacing = 4 };
    private readonly TextBlock _mapTotal = new() { Opacity = 0.7, HorizontalAlignment = HorizontalAlignment.Right };
    private readonly Canvas _map = new() { Height = 380 };
    private readonly StackPanel _recs = new() { Spacing = 6 };
    private readonly TextBlock _footer = new() { FontWeight = FontWeights.SemiBold };
    private readonly TextBlock _shell = new() { Opacity = 0.7 };
    private readonly TextBox _script = new()
    {
        IsReadOnly = true,
        AcceptsReturn = true,
        TextWrapping = TextWrapping.NoWrap,
        FontFamily = new FontFamily("Cascadia Mono, Consolas"),
        FontSize = 12,
        Height = 200,
    };
    private readonly Button _copy = new() { Content = "Copy script" };
    private readonly TextBlock _hint = new() { Opacity = 0.7, TextWrapping = TextWrapping.Wrap };

    public DiskUsageView(ServerViewModel vm, AppEnvironment env)
    {
        _vm = vm;
        _model = new DiskUsageModel(vm.Node, env.DiskScanners.Create(vm.Node), env.DiskScans,
            env.Collector.UiMarshaller, scope: DiskUsageModel.DefaultScope(vm.HostOs));

        _jobCard = Card(_jobStrip);
        _progressCard = Card(_progress);
        Content = new ScrollViewer { Content = BuildLayout() };

        foreach (var s in DiskUsageModel.Scopes(vm.Disk.Mounts.Select(m => m.Mount))) _scope.Items.Add(s);
        if (!_scope.Items.Contains(_model.Scope)) _scope.Items.Insert(0, _model.Scope);
        _scope.SelectedItem = _model.Scope;
        _scope.SelectionChanged += (_, _) =>
        {
            if (_scope.SelectedItem is not string s || s == _model.Scope) return;
            _model.Scope = s;
            _focusPath.Clear();
            _model.LoadLatest();
            Refresh();
        };
        _rescan.Click += (_, _) => _ = _model.StartAsync();
        _cancel.Click += (_, _) => _model.Cancel();
        _copy.Click += OnCopy;

        _model.PropertyChanged += OnModelChanged;
        _model.SelectionChanged += (_, _) => DispatcherQueue.TryEnqueue(RefreshSelection);

        _clock = DispatcherQueue.GetForCurrentThread().CreateTimer();
        _clock.Interval = TimeSpan.FromSeconds(1);
        _clock.Tick += (_, _) => _status.Text = _model.StatusText;
        Unloaded += (_, _) => _clock.Stop();
        Loaded += (_, _) => Refresh();

        _map.SizeChanged += (_, _) => DrawMap();
        _model.LoadLatest();
        Refresh();
    }

    /// <summary>Stop a scan that runs. The window calls it when it closes.</summary>
    public void Stop() => _model.Cancel();

    private UIElement BuildLayout()
    {
        var root = new StackPanel { Spacing = 12, Padding = new Thickness(0, 4, 0, 12) };

        var header = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        header.Children.Add(new TextBlock { Text = "Scope", VerticalAlignment = VerticalAlignment.Center });
        header.Children.Add(_scope);
        header.Children.Add(_status);
        var buttons = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, HorizontalAlignment = HorizontalAlignment.Right };
        _rescan.Style = (Style)Application.Current.Resources["AccentButtonStyle"];
        buttons.Children.Add(_rescan);
        buttons.Children.Add(_cancel);
        var headerRow = new Grid();
        headerRow.Children.Add(header);
        headerRow.Children.Add(buttons);
        root.Children.Add(headerRow);

        root.Children.Add(_jobCard);
        root.Children.Add(_error);
        root.Children.Add(_stale);

        for (var i = 0; i < 4; i++)
            _tiles.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        root.Children.Add(_tiles);
        root.Children.Add(_progressCard);

        var body = new Grid { ColumnSpacing = 12 };
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(3, GridUnitType.Star) });
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(2, GridUnitType.Star), MinWidth = 280 });

        var explorer = new StackPanel { Spacing = 8 };
        var title = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        title.Children.Add(new TextBlock { Text = "Explorer", FontWeight = FontWeights.SemiBold });
        title.Children.Add(new TextBlock { Text = "Click a folder to open it.", Opacity = 0.7 });
        explorer.Children.Add(title);
        var crumbRow = new Grid();
        crumbRow.Children.Add(_crumbs);
        crumbRow.Children.Add(_mapTotal);
        explorer.Children.Add(crumbRow);
        explorer.Children.Add(_map);
        explorer.Children.Add(Legend());
        body.Children.Add(Card(explorer));

        var recPanel = new StackPanel { Spacing = 8 };
        recPanel.Children.Add(new TextBlock { Text = "Recommendations", FontWeight = FontWeights.SemiBold });
        recPanel.Children.Add(_recs);
        var recCard = Card(recPanel);
        Grid.SetColumn(recCard, 1);
        body.Children.Add(recCard);
        root.Children.Add(body);

        var cleanup = new StackPanel { Spacing = 8 };
        var footerRow = new Grid();
        footerRow.Children.Add(_footer);
        _shell.HorizontalAlignment = HorizontalAlignment.Right;
        footerRow.Children.Add(_shell);
        cleanup.Children.Add(footerRow);
        cleanup.Children.Add(_script);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        var selectSafe = new Button { Content = "Select safe" };
        selectSafe.Click += (_, _) => _model.SelectSafe();
        var clear = new Button { Content = "Clear" };
        clear.Click += (_, _) => _model.ClearSelection();
        _copy.Style = (Style)Application.Current.Resources["AccentButtonStyle"];
        actions.Children.Add(selectSafe);
        actions.Children.Add(clear);
        actions.Children.Add(_copy);
        cleanup.Children.Add(actions);
        cleanup.Children.Add(_hint);
        root.Children.Add(Card(cleanup));
        return root;
    }

    private void OnModelChanged(object? sender, PropertyChangedEventArgs e)
    {
        if (e.PropertyName == nameof(DiskUsageModel.Result)) _focusPath.Clear();
        DispatcherQueue.TryEnqueue(Refresh);
    }

    private void Refresh()
    {
        var running = _model.Phase == DiskUsagePhase.Running;
        _status.Text = _model.StatusText;
        if (running) _clock.Start(); else _clock.Stop();
        _rescan.Visibility = running ? Visibility.Collapsed : Visibility.Visible;
        _rescan.Content = _model.Result is null ? "Scan" : "Rescan";
        _cancel.Visibility = running ? Visibility.Visible : Visibility.Collapsed;
        _scope.IsEnabled = !running;

        _error.IsOpen = _model.Phase == DiskUsagePhase.Failed && !string.IsNullOrEmpty(_model.Error);
        _error.Title = "The scan failed";
        _error.Message = _model.Error ?? "";
        _stale.IsOpen = !running && _model.StaleText != null;
        _stale.Message = _model.StaleText ?? "";

        RefreshJob();
        RefreshProgress(running);
        RefreshSelection();
    }

    // The tiles, the map, the recommendations and the script depend on the selection.
    private void RefreshSelection()
    {
        RefreshTiles();
        DrawCrumbs();
        DrawMap();
        RefreshRecs();
        RefreshScript();
    }

    private void RefreshJob()
    {
        _jobCard.Visibility = _model.IsRemote ? Visibility.Visible : Visibility.Collapsed;
        _jobStrip.Children.Clear();
        _jobStrip.Children.Add(new TextBlock { Text = "Via server", FontWeight = FontWeights.Bold });
        var steps = _model.JobSteps;
        if (steps.Count == 0)
        {
            _jobStrip.Children.Add(new TextBlock { Text = "The server sends the scan to the sampler of this node.", Opacity = 0.7 });
            return;
        }
        for (var i = 0; i < steps.Count; i++)
        {
            var s = steps[i];
            var cell = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, Opacity = s.Reached ? 1 : 0.45 };
            cell.Children.Add(new TextBlock { Text = s.Label });
            if (s.Detail.Length > 0) cell.Children.Add(new TextBlock { Text = s.Detail, Opacity = 0.7 });
            _jobStrip.Children.Add(cell);
            if (i < steps.Count - 1) _jobStrip.Children.Add(new TextBlock { Text = "→", Opacity = 0.5 });
        }
    }

    private void RefreshTiles()
    {
        _tiles.Children.Clear();
        var running = _model.Phase == DiskUsagePhase.Running;
        var r = _model.Result;
        var p = _model.Progress;

        if (running)
        {
            AddTile(0, "Scanned so far", Rgb(0x8E, 0x8E, 0x93),
                p is null ? "—" : DiskSize.Format(p.Bytes), p is null ? "" : $"{DiskUsageModel.Count(p.Files)} files");
            AddTile(1, "Selected", Green, "—", "after scan");
            AddTile(2, "No access", Color.FromArgb(0xFF, 0xFF, 0x9F, 0x0A),
                p is null ? "—" : DiskUsageModel.Count(p.NoAccessDirs), "folders so far");
        }
        else
        {
            AddTile(0, "Scanned", Rgb(0x8E, 0x8E, 0x93),
                r is null ? "—" : DiskSize.Format(r.ScannedBytes), r is null ? "" : $"{DiskUsageModel.Count(r.Files)} files");
            var n = r is null ? 0 : _model.Selected.Count;
            AddTile(1, "Selected", Green, r is null ? "—" : DiskSize.Format(_model.SelectedBytes),
                r is null ? "" : $"{n} {(n == 1 ? "item" : "items")}");
            AddTile(2, "No access", Color.FromArgb(0xFF, 0xFF, 0x9F, 0x0A),
                r?.NoAccess is { } na ? (na.BytesEst is long b ? DiskSize.Format(b) : DiskUsageModel.Count(na.Count)) : "—",
                r is null ? "" : NoAccessSub(r.NoAccess));
        }

        var fs = r?.Fs;
        var blue = Color.FromArgb(0xFF, 0x5B, 0x8F, 0xD1);
        if (fs is null)
            AddTile(3, "Disk free", blue, "—", "");
        else if (_model.Scope == DiskUsageModel.HomeScope || r!.Root != fs.Mount)
            AddTile(3, "Not scanned", blue, DiskSize.Format(Math.Max(0, fs.Used - r!.ScannedBytes)),
                $"outside {_model.Scope} · system & apps");
        else
            AddTile(3, "Disk free", blue, DiskSize.Format(fs.Free), $"of {DiskSize.Format(fs.Total)} · {fs.Mount}");
    }

    private static string NoAccessSub(DiskScanNoAccess na)
    {
        if (na.Count == 0) return "none";
        var dirs = na.Dirs ?? Array.Empty<string>();
        if (dirs.Count == 0) return $"{na.Count} folders";
        var more = na.Count - 1;
        return more > 0 ? $"{dirs[0]} +{more}" : dirs[0];
    }

    private static Color Rgb(byte r, byte g, byte b) => Color.FromArgb(0xFF, r, g, b);

    private void AddTile(int col, string label, Color dot, string value, string sub)
    {
        var panel = new StackPanel { Spacing = 2 };
        var head = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        head.Children.Add(new Ellipse { Width = 8, Height = 8, Fill = new SolidColorBrush(dot), VerticalAlignment = VerticalAlignment.Center });
        head.Children.Add(new TextBlock { Text = label, Opacity = 0.7 });
        panel.Children.Add(head);
        panel.Children.Add(new TextBlock { Text = value, FontSize = 20, FontWeight = FontWeights.Bold });
        panel.Children.Add(new TextBlock { Text = sub, Opacity = 0.6, FontSize = 12, TextTrimming = TextTrimming.CharacterEllipsis });
        var card = Card(panel);
        Grid.SetColumn(card, col);
        _tiles.Children.Add(card);
    }

    private void RefreshProgress(bool running)
    {
        _progressCard.Visibility = running ? Visibility.Visible : Visibility.Collapsed;
        _progress.Children.Clear();
        if (!running) return;
        var p = _model.Progress;
        _progress.Children.Add(new TextBlock
        {
            Text = $"Scanning {_model.Scope} on {_vm.Node.DisplayName}",
            FontSize = 18,
            FontWeight = FontWeights.Bold,
        });
        if (p is null)
        {
            _progress.Children.Add(new ProgressBar { IsIndeterminate = true });
            _progress.Children.Add(new TextBlock { Text = _model.IsRemote ? "Waiting for the sampler…" : "Starting the sampler…", Opacity = 0.7 });
            return;
        }
        var bar = new ProgressBar { Minimum = 0, Maximum = 1 };
        if (p.UsedBytes > 0) bar.Value = Math.Min(1, (double)p.Bytes / p.UsedBytes);
        else bar.IsIndeterminate = true;
        _progress.Children.Add(bar);
        var used = p.UsedBytes > 0 ? $" of {DiskSize.Format(p.UsedBytes)} used" : "";
        _progress.Children.Add(new TextBlock
        {
            Text = $"{DiskSize.Format(p.Bytes)}{used} · {DiskUsageModel.Count(p.Files)} files · " +
                   $"{DiskUsageModel.Count(p.NoAccessDirs)} no access · {DiskUsageModel.Elapsed(TimeSpan.FromMilliseconds(p.ElapsedMs))}",
            Opacity = 0.8,
        });
        _progress.Children.Add(new TextBlock
        {
            Text = "Now reading " + p.Current,
            Opacity = 0.7,
            FontFamily = new FontFamily("Cascadia Mono, Consolas"),
            FontSize = 12,
            TextTrimming = TextTrimming.CharacterEllipsis,
        });
        var top = p.Top ?? Array.Empty<DiskScanTop>();
        if (top.Count == 0) return;
        _progress.Children.Add(new TextBlock { Text = "Largest so far", FontWeight = FontWeights.SemiBold });
        var max = Math.Max(1, top.Max(t => t.B));
        foreach (var t in top.Take(6))
        {
            var row = new Grid { ColumnSpacing = 8 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(2, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(3, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(70) });
            row.Children.Add(new TextBlock { Text = t.Path, TextTrimming = TextTrimming.CharacterEllipsis, FontSize = 12 });
            var track = new ProgressBar { Minimum = 0, Maximum = max, Value = t.B, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(track, 1);
            row.Children.Add(track);
            var size = new TextBlock { Text = DiskSize.Format(t.B), HorizontalAlignment = HorizontalAlignment.Right, FontSize = 12 };
            Grid.SetColumn(size, 2);
            row.Children.Add(size);
            _progress.Children.Add(row);
        }
    }

    // Treemap

    private DiskNode? FocusNode => _focusPath.Count > 0 ? _focusPath[^1] : _model.Result?.Tree;

    private void DrawCrumbs()
    {
        _crumbs.Children.Clear();
        var root = _model.Result?.Tree;
        if (root is null) { _mapTotal.Text = ""; return; }
        var chain = new List<DiskNode> { root };
        chain.AddRange(_focusPath);
        for (var i = 0; i < chain.Count; i++)
        {
            if (i > 0) _crumbs.Children.Add(new TextBlock { Text = "›", Opacity = 0.5, VerticalAlignment = VerticalAlignment.Center });
            var depth = i;
            var link = new HyperlinkButton { Content = chain[i].N, Padding = new Thickness(2, 0, 2, 0) };
            link.Click += (_, _) =>
            {
                _focusPath.RemoveRange(depth, _focusPath.Count - depth);
                DrawCrumbs();
                DrawMap();
            };
            _crumbs.Children.Add(link);
        }
        _mapTotal.Text = DiskSize.Format(FocusNode!.B);
    }

    private void DrawMap()
    {
        _map.Children.Clear();
        var focus = FocusNode;
        var w = _map.ActualWidth;
        var h = _map.ActualHeight;
        if (w <= 0 || h <= 0) return;
        if (focus is null)
        {
            _map.Children.Add(new TextBlock
            {
                Text = _model.Phase == DiskUsagePhase.Running ? "The map shows after the scan." : "Scan to see what uses the disk.",
                Opacity = 0.6,
                Margin = new Thickness(8),
            });
            return;
        }

        var selected = _model.SelectedNodeIds;
        var frame = new TreemapRect(0, 0, w, h);
        foreach (var top in TreemapLayout.Squarify(focus.Children, n => n.B, frame))
        {
            var node = top.Item;
            var r = top.Rect.Inset(1, 1, 1, 1);
            var hasKids = node.Children.Count > 0 && !node.F;
            AddTile(node, r, parent: hasKids, selected);
            if (!hasKids || r.Width < 30 || r.Height < 40) continue;
            var inner = r.Inset(3, 20, 3, 3);
            foreach (var leaf in TreemapLayout.Squarify(node.Children, n => n.B, inner))
                AddTile(leaf.Item, leaf.Rect.Inset(1, 1, 1, 1), parent: false, selected, opens: node);
        }
    }

    // A click on a tile opens the folder: the tile itself when it has children, else its parent.
    private void AddTile(DiskNode node, TreemapRect r, bool parent, IReadOnlySet<string> selected, DiskNode? opens = null)
    {
        if (r.Width <= 0 || r.Height <= 0) return;
        var isSelected = node.Id != null && selected.Contains(node.Id);
        var noAccess = node.Cat == "none";
        var color = noAccess ? NoAccessGray : CategoryColor(node.Cat);
        Brush fill = isSelected ? Stripes(Green, 0.6, 0.15)
            : noAccess ? Stripes(NoAccessGray, 0.35, 0.08)
            : new SolidColorBrush(WithAlpha(color, parent ? 0.14 : 0.3));
        var rect = new Rectangle
        {
            Width = r.Width,
            Height = r.Height,
            Fill = fill,
            Stroke = new SolidColorBrush(WithAlpha(isSelected ? Green : color, 0.6)),
            StrokeThickness = 1,
            RadiusX = 3,
            RadiusY = 3,
        };
        Canvas.SetLeft(rect, r.X);
        Canvas.SetTop(rect, r.Y);
        var label = noAccess ? $"{node.N} · No access" : node.N;
        ToolTipService.SetToolTip(rect, $"{label}\n{DiskSize.Format(node.B)}");

        var target = node.Children.Count > 0 && !node.F ? node : opens;
        if (target != null && !node.O)
        {
            rect.Tapped += (_, _) => Open(target);
        }
        _map.Children.Add(rect);

        if (r.Width < 48 || r.Height < 18) return;
        var text = new TextBlock
        {
            Text = r.Height >= 34 && !parent ? $"{node.N}\n{DiskSize.Format(node.B)}" : $"{node.N}  {DiskSize.Format(node.B)}",
            FontSize = 11,
            MaxWidth = Math.Max(0, r.Width - 8),
            TextTrimming = TextTrimming.CharacterEllipsis,
            IsHitTestVisible = false,
            FontWeight = parent ? FontWeights.SemiBold : FontWeights.Normal,
        };
        Canvas.SetLeft(text, r.X + 4);
        Canvas.SetTop(text, r.Y + 3);
        _map.Children.Add(text);
    }

    private void Open(DiskNode target)
    {
        var focus = FocusNode;
        if (focus is null) return;
        if (!focus.Children.Contains(target))
        {
            // A leaf of the second level: open its parent first.
            var parent = focus.Children.FirstOrDefault(c => c.Children.Contains(target));
            if (parent is null) return;
            _focusPath.Add(parent);
        }
        _focusPath.Add(target);
        DrawCrumbs();
        DrawMap();
    }

    private static Color CategoryColor(string cat)
    {
        foreach (var c in Categories) if (c.Cat == cat) return c.Color;
        return Categories[^1].Color;
    }

    private static Color WithAlpha(Color c, double alpha) => Color.FromArgb((byte)(alpha * 255), c.R, c.G, c.B);

    // Diagonal stripes: a repeated hard-stop gradient.
    private static Brush Stripes(Color c, double strong, double weak)
    {
        var brush = new LinearGradientBrush
        {
            MappingMode = BrushMappingMode.Absolute,
            SpreadMethod = GradientSpreadMethod.Repeat,
            StartPoint = new Windows.Foundation.Point(0, 0),
            EndPoint = new Windows.Foundation.Point(4.2, 4.2),
        };
        brush.GradientStops.Add(new GradientStop { Color = WithAlpha(c, strong), Offset = 0 });
        brush.GradientStops.Add(new GradientStop { Color = WithAlpha(c, strong), Offset = 0.5 });
        brush.GradientStops.Add(new GradientStop { Color = WithAlpha(c, weak), Offset = 0.5 });
        brush.GradientStops.Add(new GradientStop { Color = WithAlpha(c, weak), Offset = 1 });
        return brush;
    }

    private static UIElement Legend()
    {
        var wrap = new VariableSizedWrapGrid { Orientation = Orientation.Horizontal, ItemWidth = 160 };
        foreach (var (_, color, label) in Categories)
            wrap.Children.Add(Swatch(new SolidColorBrush(WithAlpha(color, 0.6)), label));
        wrap.Children.Add(Swatch(Stripes(NoAccessGray, 0.6, 0.15), "No access"));
        wrap.Children.Add(Swatch(Stripes(Green, 0.6, 0.15), "Selected for cleanup"));
        return wrap;
    }

    private static UIElement Swatch(Brush fill, string label)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, Margin = new Thickness(0, 2, 12, 2) };
        row.Children.Add(new Rectangle { Width = 10, Height = 10, RadiusX = 3, RadiusY = 3, Fill = fill, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = label, FontSize = 12, Opacity = 0.8 });
        return row;
    }

    // Recommendations and the script

    private void RefreshRecs()
    {
        _recs.Children.Clear();
        var r = _model.Result;
        if (r is null || r.Recommendations.Count == 0)
        {
            _recs.Children.Add(new TextBlock
            {
                Text = r is null ? "Recommendations show after the scan." : "No recommendations for this scope.",
                Opacity = 0.6,
            });
            return;
        }
        _recs.Children.Add(new TextBlock { Text = "Select items to build a cleanup script", Opacity = 0.7 });
        AddGroup(r, DiskRec.SafeGroup, "Safe to remove",
            Color.FromArgb(0xFF, 0x17, 0x3A, 0x22), Color.FromArgb(0xFF, 0x7E, 0xE2, 0x9A));
        AddGroup(r, DiskRec.ReviewGroup, "Review first",
            Color.FromArgb(0xFF, 0x3D, 0x2A, 0x12), Color.FromArgb(0xFF, 0xFF, 0xB8, 0x77));
    }

    private void AddGroup(DiskScanResult result, string group, string title, Color chipBack, Color chipText)
    {
        var recs = result.Recommendations.Where(x => x.Group == group).ToList();
        if (recs.Count == 0) return;
        _recs.Children.Add(Chip(title, chipBack, chipText, top: 6));
        foreach (var rec in recs)
        {
            var row = new Grid { ColumnSpacing = 8 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

            var box = new CheckBox { IsChecked = _model.Selected.Contains(rec.Id), MinWidth = 0, VerticalAlignment = VerticalAlignment.Top };
            var id = rec.Id;
            box.Click += (_, _) => _model.Toggle(id);
            row.Children.Add(box);

            var text = new StackPanel { Spacing = 1 };
            var title1 = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
            title1.Children.Add(new TextBlock { Text = rec.Label, TextTrimming = TextTrimming.CharacterEllipsis });
            if (rec.Elevated)
                title1.Children.Add(Chip(result.IsWindows ? "admin" : "sudo",
                    Color.FromArgb(0xFF, 0x3D, 0x2A, 0x12), Color.FromArgb(0xFF, 0xFF, 0xB8, 0x77)));
            text.Children.Add(title1);
            text.Children.Add(new TextBlock
            {
                Text = rec.Path,
                FontSize = 11,
                Opacity = 0.6,
                FontFamily = new FontFamily("Cascadia Mono, Consolas"),
                TextTrimming = TextTrimming.CharacterEllipsis,
            });
            Grid.SetColumn(text, 1);
            row.Children.Add(text);

            var size = new TextBlock { Text = DiskSize.Format(rec.Bytes), FontWeight = FontWeights.SemiBold };
            Grid.SetColumn(size, 2);
            row.Children.Add(size);
            _recs.Children.Add(row);
        }
    }

    private static Border Chip(string text, Color back, Color fore, double top = 0) => new()
    {
        Background = new SolidColorBrush(back),
        CornerRadius = new CornerRadius(4),
        Padding = new Thickness(6, 1, 6, 1),
        Margin = new Thickness(0, top, 0, 0),
        HorizontalAlignment = HorizontalAlignment.Left,
        VerticalAlignment = VerticalAlignment.Center,
        Child = new TextBlock { Text = text, FontSize = 11, FontWeight = FontWeights.SemiBold, Foreground = new SolidColorBrush(fore) },
    };

    private void RefreshScript()
    {
        var script = _model.Script;
        var n = script?.Chosen.Count ?? 0;
        _footer.Text = $"{DiskSize.Format(script?.TotalBytes ?? 0)} selected · {n} {(n == 1 ? "item" : "items")}";
        _shell.Text = script?.ShellLabel ?? "";
        _script.Text = script?.Text ?? "";
        _copy.IsEnabled = n > 0;
        _hint.Text = _vm.Node.Kind == NodeKind.Local
            ? $"Paste into {(script?.Shell == CleanupShell.Bash ? "a terminal" : "PowerShell")} on this PC. Towertail never runs the script."
            : $"Paste into an SSH session on {_vm.Node.DisplayName}. Towertail never runs the script.";
    }

    private async void OnCopy(object sender, RoutedEventArgs e)
    {
        if (_model.Script is not { } script) return;
        var package = new DataPackage();
        package.SetText(script.Text);
        Clipboard.SetContent(package);
        _copy.Content = "Copied ✓";
        await Task.Delay(TimeSpan.FromSeconds(2));
        _copy.Content = "Copy script";
    }

    private static Border Card(UIElement child) => new()
    {
        Background = (Brush)Application.Current.Resources["CardBackgroundFillColorDefaultBrush"],
        BorderBrush = (Brush)Application.Current.Resources["CardStrokeColorDefaultBrush"],
        BorderThickness = new Thickness(1),
        CornerRadius = new CornerRadius(8),
        Padding = new Thickness(14, 12, 14, 12),
        Child = child,
    };
}
