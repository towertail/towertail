using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The panel of one process: its facts and its CPU, memory and I/O over the window of the cursor.</summary>
public sealed partial class ProcessPanel : UserControl
{
    private WorkbenchCursor? _cursor;
    private IProcessDetailSource? _source;
    private ServerViewModel? _vm;
    private CancellationTokenSource? _cts;
    private DateTime _loadedAt;

    public ProcessPanel() { InitializeComponent(); }

    public void Bind(ServerViewModel vm, WorkbenchCursor cursor, IProcessDetailSource source)
    {
        _vm = vm;
        _cursor = cursor;
        _source = source;
        cursor.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName == nameof(WorkbenchCursor.OpenProcess)) DispatcherQueue.TryEnqueue(() => { _loadedAt = DateTime.MinValue; Load(); });
            else if (a.PropertyName is nameof(WorkbenchCursor.Range) or nameof(WorkbenchCursor.Zoom)) DispatcherQueue.TryEnqueue(() => { _loadedAt = DateTime.MinValue; Load(); });
        };
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName == nameof(ServerViewModel.LastSeen) && DateTime.UtcNow - _loadedAt > TimeSpan.FromSeconds(5))
                DispatcherQueue.TryEnqueue(Load);
        };
    }

    private void OnClose(object sender, RoutedEventArgs e)
    {
        if (_cursor is not null) _cursor.OpenProcess = null;
    }

    private async void Load()
    {
        if (_cursor?.OpenProcess is not { } r || _source is null)
        {
            _cts?.Cancel();
            return;
        }
        _cts?.Cancel();
        var cts = _cts = new CancellationTokenSource();
        _loadedAt = DateTime.UtcNow;
        NameText.Text = r.Name;
        CmdText.Text = r.Cmd == r.Name ? "" : r.Cmd;
        var window = _cursor.Window(DateTime.UtcNow);
        ProcessDetail detail;
        try { detail = await _source.DetailAsync(r, window, cts.Token); }
        catch (OperationCanceledException) { return; }
        catch (Exception e)
        {
            EmptyText.Text = "Could not load this process: " + e.Message;
            EmptyText.Visibility = Visibility.Visible;
            return;
        }
        if (cts.IsCancellationRequested) return;
        var dark = WB.IsDark(this);
        var empty = detail.Cpu.Count == 0;
        EmptyText.Text = "This process is not in the data of this window.";
        EmptyText.Visibility = empty ? Visibility.Visible : Visibility.Collapsed;

        Facts.Children.Clear();
        var f = detail.Facts;
        Fact("PID", (r.Pid ?? f.Pid)?.ToString());
        Fact("User", f.User);
        Fact("Parent", f.Parent is null ? f.Ppid?.ToString() : f.Ppid is int pp ? $"{f.Parent} ({pp})" : f.Parent);
        Fact("Folder", f.Cwd);
        Fact("Repo", f.Repo);
        Fact("Ports", f.Ports is { Count: > 0 } ps ? string.Join(", ", ps) : null);
        Fact("Started", f.Started is { } s ? s.ToLocalTime().ToString("MMM d HH:mm") + " (" + ShortDuration.Age(DateTime.UtcNow - s) + " ago)" : null);
        Fact("Threads", f.Threads?.ToString());
        if (f.Stale && !empty)
        {
            var note = WB.Text("The server keeps PID, parent and folder for 1 h.", 11, false, WB.Muted);
            note.TextWrapping = TextWrapping.Wrap;
            Facts.Children.Add(note);
        }

        var cursor = _cursor;
        CpuChart.Bind(new[] { new ChartSeries("CPU", WB.Metric(RailMetric.Cpu, dark), detail.Cpu) }, window, new ChartFormat(v => $"{v:0.#}%"), cursor);
        RssChart.Bind(new[] { new ChartSeries("Memory", WB.Metric(RailMetric.Mem, dark), detail.Rss) }, window, new ChartFormat(v => WorkbenchData.Bytes(v)), cursor);
        IoChart.Bind(new[] { new ChartSeries("I/O", WB.Metric(RailMetric.Io, dark), detail.Io) }, window, new ChartFormat(v => WorkbenchData.Bytes(v) + "/s"), cursor);
    }

    private void Fact(string label, string? value)
    {
        if (string.IsNullOrEmpty(value)) return;
        var g = new Grid { ColumnSpacing = 8 };
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(64) });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        g.Children.Add(WB.Text(label, 12, false, WB.Muted));
        var v = WB.Text(value, 12, true);
        v.TextWrapping = TextWrapping.Wrap;
        Grid.SetColumn(v, 1);
        g.Children.Add(v);
        Facts.Children.Add(g);
    }
}
