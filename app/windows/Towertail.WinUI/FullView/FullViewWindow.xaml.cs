using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class FullViewWindow : Window
{
    private ServerViewModel? _vm;
    private FullViewModel _fvm = new();

    public FullViewWindow()
    {
        InitializeComponent();
        Title = "Towertail — Full View";
    }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        Title = $"Towertail — {vm.Node.DisplayName}";
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

    private void ShowTab(string tag)
    {
        if (_vm is null) return;
        Host.Children.Clear();
        UIElement content = tag switch
        {
            "cpu"   => MakeChart(_vm.CpuSeries, "CPU %"),
            "mem"   => MakeChart(_vm.MemSeries, "MEM %"),
            "disk"  => MakeChart(_vm.DiskSeries, "DISK %"),
            "net"   => MakeNet(),
            "procs" => MakeProcs(),
            _ => new TextBlock { Text = "—" },
        };
        Host.Children.Add(content);
    }

    private UIElement MakeChart(MetricSeries series, string label)
    {
        var chart = new MetricChart();
        chart.Bind(series, label);
        return chart;
    }

    private UIElement MakeNet()
    {
        if (_vm is null) return new TextBlock();
        var grid = new Grid();
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var rx = new MetricChart(); rx.Bind(_vm.RxSeries, "RX MB/s");
        var tx = new MetricChart(); tx.Bind(_vm.TxSeries, "TX MB/s");
        Grid.SetRow(rx, 0); Grid.SetRow(tx, 1);
        grid.Children.Add(rx); grid.Children.Add(tx);
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
