using System.Collections.ObjectModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;
using Windows.ApplicationModel.DataTransfer;

namespace Towertail.WinUI.FullView;

/// <summary>
/// The process list of a local or SSH host at the time of the cursor. A click opens the
/// process panel. The right-click menu ignores, copies or kills a process.
/// </summary>
public sealed partial class ProcessTable : UserControl
{
    private ServerViewModel? _vm;
    private WorkbenchCursor? _cursor;
    private readonly ObservableCollection<ProcessRow> _rows = new();
    private readonly Segmented _sortBar = new(Enum.GetValues<ProcSort>().Select(s => s.Title()));
    private ProcSort _sort = ProcSort.Cpu;
    private DateTime? _shownAt;
    private bool _refreshing;
    private bool _killInFlight;
    private bool _queued;

    public ProcessTable()
    {
        InitializeComponent();
        Items.ItemsSource = _rows;
        SortHost.Content = _sortBar;
        _sortBar.Select(0);
        _sortBar.SelectionChanged += i => Sort = (ProcSort)i;
    }

    public ProcSort Sort
    {
        get => _sort;
        set
        {
            if (_sort == value) return;
            _sort = value;
            _sortBar.Select((int)value);
            _shownAt = null;
            Refresh();
        }
    }

    public async void Bind(ServerViewModel vm, WorkbenchCursor cursor)
    {
        _vm = vm;
        _cursor = cursor;
        var env = Towertail.WinUI.App.Current.Environment;
        await env.Servers.EnsureProcsHydratedAsync(vm.Node.Id);
        Loaded += (_, _) => { _shownAt = null; Refresh(); };
        Refresh();
        vm.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(ServerViewModel.LastSeen)) Queue();
        };
        cursor.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName is nameof(WorkbenchCursor.HoverAt) or nameof(WorkbenchCursor.PinnedAt)) Queue();
            else if (args.PropertyName == nameof(WorkbenchCursor.OpenProcess)) DispatcherQueue.TryEnqueue(SyncSelection);
        };
    }

    private void Queue()
    {
        if (_queued) return;
        _queued = true;
        DispatcherQueue.TryEnqueue(() => { _queued = false; Refresh(); });
    }

    private void Refresh()
    {
        if (_vm is null || _cursor is null) return;
        if (!IsLoaded) return;
        var dark = WB.IsDark(this);
        if (_vm.ProcsSkippedTotal is int skipped)
        {
            SkippedText.Text = $"Process scan skipped: {skipped:N0} processes (over the sampler's scan limit)";
            SkippedText.Visibility = Visibility.Visible;
            Items.Visibility = Visibility.Collapsed;
            return;
        }
        SkippedText.Visibility = Visibility.Collapsed;
        Items.Visibility = Visibility.Visible;

        var snap = LocalSnapshot.From(_vm.Procs, _cursor.Effective(null), _sort);
        var live = _cursor.Cursor is null;
        WhenText.Text = snap.Label(DateTime.UtcNow);
        LivePill.Visibility = live ? Visibility.Visible : Visibility.Collapsed;
        LivePill.BorderBrush = WB.Brush(WB.Ok(dark));
        LiveText.Foreground = WB.Brush(WB.Ok(dark));
        CountText.Text = $"{snap.Rows.Count} of {snap.Total} processes";
        if (snap.T == _shownAt) return;
        _shownAt = snap.T;

        var maxCpu = Math.Max(100, snap.Rows.Select(r => r.CpuPct).DefaultIfEmpty(0).Max());
        var next = snap.Rows.Select(p => ProcessRow.From(p, maxCpu, _vm.Node is not null)).ToList();
        _refreshing = true;
        try
        {
            for (var i = 0; i < next.Count; i++)
            {
                if (i < _rows.Count) { if (_rows[i] != next[i]) _rows[i] = next[i]; }
                else _rows.Add(next[i]);
            }
            while (_rows.Count > next.Count) _rows.RemoveAt(_rows.Count - 1);
            SyncSelection();
        }
        finally { _refreshing = false; }
    }

    private void SyncSelection()
    {
        var open = _cursor?.OpenProcess;
        var was = _refreshing;
        _refreshing = true;
        try
        {
            Items.SelectedItem = open is null ? null : _rows.FirstOrDefault(r => r.Ref.Key == open.Key);
        }
        finally { _refreshing = was; }
    }

    private void OnSelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_refreshing || _cursor is null) return;
        if (Items.SelectedItem is ProcessRow row) _cursor.OpenProcess = row.Ref;
    }

    private void OnRightTapped(object sender, RightTappedRoutedEventArgs e)
    {
        if (_vm is null || (e.OriginalSource as FrameworkElement)?.DataContext is not ProcessRow row) return;
        var menu = new MenuFlyout();
        void Add(string text, Action act)
        {
            var item = new MenuFlyoutItem { Text = text };
            item.Click += (_, _) => act();
            menu.Items.Add(item);
        }
        Add("Ignore on this host", () => _ = IgnoreAsync(row.Name, _vm.Node.Id));
        Add("Ignore on all hosts", () => _ = IgnoreAsync(row.Name, null));
        menu.Items.Add(new MenuFlyoutSeparator());
        Add("Copy PID", () => Copy(row.Pid.ToString()));
        Add("Copy command", () => Copy(string.IsNullOrEmpty(row.Cmd) ? row.Name : row.Cmd));
        menu.Items.Add(new MenuFlyoutSeparator());
        Add("Kill…", () => _ = KillAsync(row.Pid, row.Name));
        menu.ShowAt((FrameworkElement)sender, e.GetPosition((UIElement)sender));
    }

    private static void Copy(string text)
    {
        var pkg = new DataPackage();
        pkg.SetText(text);
        Clipboard.SetContent(pkg);
    }

    private static Task IgnoreAsync(string name, Guid? hostId)
    {
        var env = Towertail.WinUI.App.Current.Environment;
        return new LongProcIgnore(env.ServerSettings, env.Nodes, env.Servers, env.Backend).IgnoreAsync(name, hostId);
    }

    private Task KillAsync(int pid, string name)
        => _vm is null || _killInFlight ? Task.CompletedTask
            : KillFlow.RunAsync(this, _vm.Node, pid, name, inFlight => _killInFlight = inFlight);

    private async void OnKillRequested(object sender, PidKillEventArgs e) => await KillAsync(e.Pid, e.Name);
}

/// <summary>Display projection over <see cref="ProcSample"/>. Public so that <c>x:Bind</c> can read it.</summary>
public sealed record ProcessRow(
    int Pid, string Name, string Cmd, string User, double CpuPct, string CpuText, double BarWidth,
    string RssText, string IoText, int Threads, string StartedText, bool CanKill, ProcessRef Ref)
{
    public static ProcessRow From(ProcSample p, double maxCpu, bool canKill) => new(
        p.Pid,
        string.IsNullOrEmpty(p.Name) ? "—" : p.Name,
        string.IsNullOrEmpty(p.Cmd) || p.Cmd == p.Name ? "" : p.Cmd,
        p.User ?? "—",
        p.CpuPct,
        p.CpuPct.ToString("0.0"),
        Math.Clamp(p.CpuPct / maxCpu, 0, 1) * 44,
        StringFormatters.Bytes(p.Rss),
        p.ReadBps is null && p.WriteBps is null ? "—" : StringFormatters.Bytes(Math.Max(p.ReadBps ?? 0, 0) + Math.Max(p.WriteBps ?? 0, 0)) + "/s",
        p.Threads ?? 0,
        p.StartTs is { } s ? ShortDuration.Age(DateTime.UtcNow - s) + " ago" : "—",
        canKill,
        ProcessRef.Local(p));
}
