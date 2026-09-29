using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class FullViewWindow : Window
{
    private ServerViewModel? _vm;
    private FullViewModel _fvm = new();
    private bool _paused;
    private readonly List<MetricChart> _charts = new();

    public FullViewWindow()
    {
        InitializeComponent();
        Title = "Towertail — Full View";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(AppTitleBar);
    }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        Title = $"Towertail — {vm.Node.DisplayName}";
        SubtitleText.Text = vm.Node.DisplayName;
        if (Nav.SelectedItem is null) Nav.SelectedItem = Nav.MenuItems[0];
        ShowTab("cpu");
    }

    public void SelectTab(string tag)
    {
        foreach (var item in Nav.MenuItems.OfType<NavigationViewItem>())
        {
            if (item.Tag is string s && s == tag) { Nav.SelectedItem = item; return; }
        }
    }

    private void OnNavSelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (args.SelectedItem is NavigationViewItem item && item.Tag is string tag)
            ShowTab(tag);
    }

    private void OnPlayPauseClick(object sender, RoutedEventArgs e)
    {
        _paused = !_paused;
        PlayPauseIcon.Glyph = _paused ? "\uE768" : "\uE769"; // Play / Pause
        ToolTipService.SetToolTip(PlayPauseBtn, _paused ? "Resume updates" : "Pause updates");
        foreach (var c in _charts) c.SetPaused(_paused);
    }

    private void ShowTab(string tag)
    {
        if (_vm is null) return;
        Host.Children.Clear();
        _charts.Clear();
        UIElement content = tag switch
        {
            "cpu"   => MakeChart(_vm.CpuSeries, "CPU %"),
            "mem"   => MakeChart(_vm.MemSeries, "MEM %"),
            "disk"  => MakeChart(_vm.DiskSeries, "DISK %"),
            "net"   => MakeNet(),
            "procs" => MakeProcs(),
            "health" => MakeHealth(),
            _ => new TextBlock { Text = "—" },
        };
        Host.Children.Add(content);
    }

    private UIElement MakeChart(MetricSeries series, string label)
    {
        var chart = new MetricChart();
        chart.Bind(series, label);
        chart.SetPaused(_paused);
        _charts.Add(chart);
        return chart;
    }

    private UIElement MakeNet()
    {
        if (_vm is null) return new TextBlock();
        // Layout:
        //   row 0: RX chart
        //   row 1: TX chart
        //   row 2: Processes/Ports segmented selector
        //   row 3: selected sub-table (Processes by default; Ports on toggle)
        var grid = new Grid { RowSpacing = 8 };
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });

        var rx = new MetricChart(); rx.Bind(_vm.RxSeries, "RX MB/s", isPercent: false); rx.SetPaused(_paused);
        var tx = new MetricChart(); tx.Bind(_vm.TxSeries, "TX MB/s", isPercent: false); tx.SetPaused(_paused);
        _charts.Add(rx); _charts.Add(tx);
        Grid.SetRow(rx, 0); Grid.SetRow(tx, 1);
        grid.Children.Add(rx); grid.Children.Add(tx);

        // SelectorBar with Processes/Ports — picked instead of NavigationView
        // so the selector docks tightly under the charts and doesn't grab
        // the whole row height like a NavigationView pane would.
        var selector = new SelectorBar { HorizontalAlignment = HorizontalAlignment.Stretch };
        var procsItem = new SelectorBarItem { Text = "Processes", Tag = "processes" };
        var portsItem = new SelectorBarItem { Text = "Ports", Tag = "ports" };
        selector.Items.Add(procsItem);
        selector.Items.Add(portsItem);
        selector.SelectedItem = procsItem;
        Grid.SetRow(selector, 2);
        grid.Children.Add(selector);

        var slot = new ContentControl
        {
            HorizontalContentAlignment = HorizontalAlignment.Stretch,
            VerticalContentAlignment = VerticalAlignment.Stretch,
        };
        Grid.SetRow(slot, 3);
        grid.Children.Add(slot);

        // Pre-build both tables once so toggling between them doesn't pay
        // a re-bind cost on every flip — the underlying VM is the same
        // and both controls cleanly handle being shown/hidden.
        var procs = new ProcessTable();
        procs.Bind(_vm);
        var ports = new PortsTable();
        ports.Bind(_vm);
        slot.Content = procs;

        selector.SelectionChanged += (_, _) =>
        {
            slot.Content = (selector.SelectedItem?.Tag as string) == "ports"
                ? (UIElement)ports
                : procs;
        };

        return grid;
    }

    private UIElement MakeHealth()
    {
        if (_vm is null) return new TextBlock();
        var grid = new Grid { RowSpacing = 12 };
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });

        var chart = new MetricChart();
        chart.Bind(_vm.ProcCountSeries, "Processes", isPercent: false);
        chart.SetPaused(_paused);
        _charts.Add(chart);
        grid.Children.Add(chart);

        var details = new HealthDetails();
        details.Bind(_vm);
        Grid.SetRow(details, 1);
        grid.Children.Add(details);
        return grid;
    }

    private UIElement MakeProcs()
    {
        if (_vm is null) return new TextBlock();
        var table = new ProcessTable();
        table.Bind(_vm);
        return table;
    }
}
