using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;
using Windows.System;

namespace Towertail.WinUI.FullView;

/// <summary>
/// The host workbench: a header, six tabs, a range control, a live badge and a process panel.
/// The cursor of the window is shared by the charts, the rail and the tables.
/// </summary>
public sealed partial class FullViewWindow : Window
{
    private static readonly HostTab[] Tabs = Enum.GetValues<HostTab>();

    private ServerViewModel? _vm;
    private readonly WorkbenchCursor _cursor = new();
    private WorkbenchData? _data;
    private CloudHistoryCache? _cache;
    private WorkbenchSeries? _series;
    private ProcessSnapshotLoader? _snapshots;
    private readonly Segmented _tabBar = new(Tabs.Select(t => t.Title()), mono: true);
    private readonly Segmented _rangeBar = new(Enum.GetValues<HostRange>().Select(r => r.Label()), mono: true);
    private readonly Dictionary<HostTab, UIElement> _built = new();
    private readonly DispatcherTimer _timer = new() { Interval = TimeSpan.FromSeconds(1) };
    private HostTab _tab = HostTab.Overview;

    public WorkbenchCursor Cursor => _cursor;
    private bool _portsShown;
    private bool _usageShown;
    private bool _leased;

    public FullViewWindow()
    {
        InitializeComponent();
        Title = "Towertail — Full View";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(AppTitleBar);
        TabsHost.Content = _tabBar;
        RangeHost.Content = _rangeBar;
        _tabBar.SelectionChanged += i => ShowTab(Tabs[i]);
        _rangeBar.SelectionChanged += i => _cursor.Range = (HostRange)i;
        _cursor.PropertyChanged += OnCursorChanged;
        AddAccelerators();
        _timer.Tick += (_, _) => Tick();
        _timer.Start();
        Closed += (_, _) =>
        {
            _timer.Stop();
            _snapshots?.Cancel();
            (_built.GetValueOrDefault(HostTab.Disks) as DisksTab)?.Stop();
            ReportLease(false);
        };
        Activated += (_, e) =>
        {
            if (e.WindowActivationState != WindowActivationState.Deactivated) ReportLease();
        };
    }

    /// <summary>
    /// Tells the cloud lease which host and detail tab this window shows. Only cloud hosts
    /// use it. A window that closes, or a host that leaves the cloud, gives the lease back.
    /// </summary>
    private void ReportLease(bool visible = true)
    {
        if (_vm is null) return;
        var node = _vm.Node;
        if (!node.IsCloud) visible = false;
        if (!visible && !_leased) return;
        var tab = DetailTabMap.For(_tab, _portsShown, _usageShown);
        App.Current.Environment.Cloud.Lease.SetFullView(this, node.Id, tab, visible);
        _leased = visible;
    }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        var env = App.Current.Environment;
        _cache = env.HistoryFor(vm.Node);
        _series = _cache is null ? null : new WorkbenchSeries(_cache);
        _snapshots = _cache is null ? null : new ProcessSnapshotLoader(vm.Node.Id, _cache, hostLabel: vm.Node.DisplayName);
        _data = new WorkbenchData(vm, env.History, _cursor, _series);
        if (_series is not null) _series.Changed += () => DispatcherQueue.TryEnqueue(UpdateHistoryNote);
        Title = $"Towertail — {vm.Node.DisplayName}";
        SubtitleText.Text = vm.Node.DisplayName;
        HostName.Text = vm.Node.DisplayName;
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(ServerViewModel.HealthScore) or nameof(ServerViewModel.FactsLine) or nameof(ServerViewModel.Health))
                DispatcherQueue.TryEnqueue(UpdateHeader);
        };
        _rangeBar.Select((int)_cursor.Range);
        UpdateHeader();
        Tick();
        _tabBar.Select((int)_tab);
        ShowTab(_tab);
    }

    public void SelectTab(HostTab tab, RailMetric? rail = null)
    {
        _tabBar.Select((int)tab);
        if (rail is { } r) Overview().Rail = r;
        ShowTab(tab);
    }

    private OverviewTab Overview() => (OverviewTab)Build(HostTab.Overview);

    private UIElement Build(HostTab tab)
    {
        if (_built.TryGetValue(tab, out var existing)) return existing;
        var vm = _vm!;
        var data = _data!;
        UIElement view;
        switch (tab)
        {
            case HostTab.Overview:
                var o = new OverviewTab();
                o.Bind(vm, _cursor, data, _snapshots);
                view = o;
                break;
            case HostTab.Processes:
                var p = new ProcessesTab();
                p.Bind(vm, _cursor, _cache);
                p.PortsShown += shown => { _portsShown = shown; ReportLease(); };
                view = p;
                break;
            case HostTab.Health:
                var h = new HealthTab();
                h.Bind(vm, _cursor, data);
                view = h;
                break;
            case HostTab.Disks:
                var d = new DisksTab();
                d.Bind(vm, _cursor, data);
                d.UsageShown += shown => { _usageShown = shown; ReportLease(); };
                view = d;
                break;
            case HostTab.Alerts:
                var a = new AlertsTab();
                a.Bind(vm);
                view = a;
                break;
            default:
                var s = new SettingsTab();
                s.Bind(vm);
                view = s;
                break;
        }
        return _built[tab] = view;
    }

    private void ShowTab(HostTab tab)
    {
        if (_vm is null) return;
        _tab = tab;
        Host.Children.Clear();
        Host.Children.Add(Build(tab));
        if (!_panelBound)
        {
            Panel.Bind(_vm, _cursor, _cache is null ? new LocalProcessSource(_vm) : new CloudProcessSource(_vm.Node.Id, _cache));
            _panelBound = true;
        }
        UpdatePanel();
        ReportLease();
    }

    private bool _panelBound;

    private void UpdatePanel()
    {
        var show = _cursor.OpenProcess is not null && _tab is not (HostTab.Alerts or HostTab.Settings);
        Panel.Visibility = show ? Visibility.Visible : Visibility.Collapsed;
    }

    private void OnCursorChanged(object? sender, System.ComponentModel.PropertyChangedEventArgs e)
    {
        DispatcherQueue.TryEnqueue(() =>
        {
            switch (e.PropertyName)
            {
                case nameof(WorkbenchCursor.OpenProcess):
                    UpdatePanel();
                    break;
                case nameof(WorkbenchCursor.Range):
                    _rangeBar.Select((int)_cursor.Range);
                    break;
                case nameof(WorkbenchCursor.Zoom):
                    UpdateZoom();
                    break;
                case nameof(WorkbenchCursor.PinnedAt):
                    Tick();
                    break;
            }
        });
    }

    private void UpdateZoom()
    {
        if (_cursor.Zoom is { } z)
        {
            ResetZoomBtn.Content = WB.Text($"Reset zoom · {z.From.ToLocalTime():HH:mm} → {z.To.ToLocalTime():HH:mm}", 12, true);
            ResetZoomBtn.Visibility = Visibility.Visible;
            _rangeBar.Select(-1);
        }
        else
        {
            ResetZoomBtn.Visibility = Visibility.Collapsed;
            _rangeBar.Select((int)_cursor.Range);
        }
    }

    private void OnResetZoom(object sender, RoutedEventArgs e) => _cursor.ResetZoom();

    private void OnLiveBadge(object sender, RoutedEventArgs e)
    {
        if (_cursor.PinnedAt is not null) _cursor.PinnedAt = null;
    }

    private void UpdateHeader()
    {
        if (_vm is null) return;
        var dark = WB.IsDark(Root);
        var hs = _vm.HealthScore;
        var brush = WB.Brush(WB.Level(hs.Level, dark));
        StatusDot.Fill = brush;
        HealthFlag.BorderBrush = brush;
        HealthFlagText.Foreground = brush;
        HealthFlagText.Text = $"HEALTH {hs.Score:0}" + (hs.Worst is { } w && hs.Level != HealthLevel.Nominal ? " · " + w.Label.ToUpperInvariant() : "");
        FactsText.Text = _vm.FactsLine ?? _vm.Node.UserAtHost;
    }

    /// <summary>The state of the REST history of a cloud host: an error, a plan limit or a load.</summary>
    private void UpdateHistoryNote()
    {
        var text = _series switch
        {
            null => null,
            { Error: { } e } => e,
            { Clamped: true } => "The history of your plan is shorter than this range.",
            { Loading: true } => "Loading the history…",
            _ => null,
        };
        HistoryNote.Text = text ?? "";
        HistoryNote.Visibility = text is null ? Visibility.Collapsed : Visibility.Visible;
    }

    /// <summary>Every second: the live badge and the ranges that have data.</summary>
    private void Tick()
    {
        if (_vm is null) return;
        var dark = WB.IsDark(Root);
        if (_cursor.PinnedAt is { } pin)
        {
            LiveDot.Fill = WB.Brush(WB.Pin(dark));
            LiveText.Text = $"◆ Pinned {pin.ToLocalTime():HH:mm:ss} · Esc";
        }
        else
        {
            LiveDot.Fill = WB.Brush(WB.Ok(dark));
            LiveText.Text = "Live · updated " + WB.Ago(_vm.LastSeen);
        }
        var now = DateTime.UtcNow;
        DateTime? oldest = _vm.CpuSeries.Points.Count > 0 ? _vm.CpuSeries.Points[0].T : null;
        foreach (var r in Enum.GetValues<HostRange>())
            _rangeBar.SetEnabled((int)r, _series is not null || WorkbenchCursor.IsAvailable(r, oldest, now));
        UpdateHistoryNote();
    }

    private void AddAccelerators()
    {
        void Add(VirtualKey key, Func<bool> act)
        {
            var acc = new KeyboardAccelerator { Key = key };
            acc.Invoked += (_, args) =>
            {
                if (TextInputFocused()) return;
                args.Handled = act();
            };
            Root.KeyboardAccelerators.Add(acc);
        }
        Add(VirtualKey.Escape, () => _cursor.HandleEscape());
        Add(VirtualKey.Left, () => _cursor.StepPin(-1, _cursor.HistoryStep(DateTime.UtcNow), DateTime.UtcNow));
        Add(VirtualKey.Right, () => _cursor.StepPin(1, _cursor.HistoryStep(DateTime.UtcNow), DateTime.UtcNow));
        Add(VirtualKey.Space, () =>
        {
            _cursor.PinLatest(_vm?.Procs.Latest?.T ?? _vm?.LastSeen);
            return true;
        });
    }

    private bool TextInputFocused()
        => Root.XamlRoot is { } xr && FocusManager.GetFocusedElement(xr) is TextBox or PasswordBox or NumberBox or ComboBox or RichEditBox;
}
