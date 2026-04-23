using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Towertail.WinUI.Design;
using Towertail.WinUI.FullView;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cards;

public sealed partial class ServerCardView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty =
        DependencyProperty.Register(nameof(ViewModel), typeof(ServerViewModel),
            typeof(ServerCardView), new PropertyMetadata(null, OnViewModelChanged));

    public ServerViewModel? ViewModel
    {
        get => (ServerViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    public ServerCardView()
    {
        InitializeComponent();
    }

    private static void OnViewModelChanged(DependencyObject d, DependencyPropertyChangedEventArgs e)
    {
        if (d is not ServerCardView c) return;
        c.Bind();
    }

    private void Bind()
    {
        if (ViewModel is null) return;
        NameText.Text = ViewModel.Node.DisplayName;
        HostText.Text = ViewModel.Node.UserAtHost;
        // Assign series ONCE so SparklineCanvas's CollectionChanged
        // subscription on series.Points is stable. Rebinding every tick
        // unsubscribes-and-resubscribes, which races with Append().
        CpuCell.Series = ViewModel.CpuSeries; CpuCell.Format = "pct";
        MemCell.Series = ViewModel.MemSeries; MemCell.Format = "pct";
        DiskCell.Series = ViewModel.DiskSeries; DiskCell.Format = "pct";
        NetCell.Series = ViewModel.NetSeries; NetCell.Format = "mbps";
        Update();
        ViewModel.PropertyChanged += (_, _) => Update();
    }

    private void Update()
    {
        if (ViewModel is null) return;
        LastSeenText.Text = StringFormatters.RelativeTime(ViewModel.LastSeen);
        CpuCell.Value = ViewModel.CpuPct;
        MemCell.Value = ViewModel.MemPct;
        DiskCell.Value = ViewModel.DiskMaxPct;
        NetCell.Value = (ViewModel.RxMBps ?? 0) + (ViewModel.TxMBps ?? 0);
    }

    private void OnCardTapped(object sender, TappedRoutedEventArgs e)
    {
        if (ViewModel is null) return;
        FullViewRegistry.Open(ViewModel);
    }
}
