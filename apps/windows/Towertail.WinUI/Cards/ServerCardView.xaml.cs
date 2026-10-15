using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Windows.UI;
using Towertail.WinUI.Design;
using Towertail.WinUI.FullView;
using Towertail.WinUI.Preferences;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

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
        // Terminal only makes sense for SSH nodes.
        TerminalBtn.Visibility = ViewModel.Node.Kind == NodeKind.Ssh
            ? Visibility.Visible
            : Visibility.Collapsed;
        UpdateFavoriteIcon();
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

        var health = ViewModel.Health;
        if (health.Level == HealthLevel.Nominal)
        {
            HealthText.Visibility = Visibility.Collapsed;
        }
        else
        {
            HealthText.Text = health.Summary;
            ToolTipService.SetToolTip(HealthText, health.Body);
            HealthText.Foreground = ThresholdTint.Brush(
                health.Level == HealthLevel.Critical ? ThresholdTint.Critical : ThresholdTint.Warn);
            HealthText.Visibility = Visibility.Visible;
        }
    }

    private void UpdateFavoriteIcon()
    {
        if (ViewModel is null) return;
        var fav = ViewModel.Node.Favorite;
        FavoriteIcon.Glyph = fav ? "\uE735" : "\uE734"; // FavoriteStarFill : FavoriteStar
        FavoriteIcon.Foreground = fav
            ? new SolidColorBrush(Color.FromArgb(0xFF, 0xF5, 0xC3, 0x00))
            : (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"];
    }

    private void OnCardTapped(object sender, TappedRoutedEventArgs e)
    {
        if (ViewModel is null) return;
        FullViewRegistry.Open(ViewModel);
    }

    private void OnHealthTapped(object sender, TappedRoutedEventArgs e)
    {
        if (ViewModel is null) return;
        e.Handled = true;
        FullViewRegistry.Open(ViewModel).SelectTab("health");
    }

    // Prevent the parent Border's Tapped (which opens FullView) from firing when an action
    // button is clicked.
    private void OnActionTapped(object sender, TappedRoutedEventArgs e) => e.Handled = true;

    private void OnTerminalClick(object sender, RoutedEventArgs e)
    {
        if (ViewModel is null) return;
        TerminalLauncher.OpenSsh(ViewModel.Node);
    }

    private void OnSettingsClick(object sender, RoutedEventArgs e)
    {
        if (ViewModel is null) return;
        var env = Towertail.WinUI.App.Current.Environment;
        var win = new ServerEditWindow();
        win.Bind(ViewModel.Node, node =>
        {
            if (node != null) env.Nodes.Update(node);
        });
        win.Activate();
    }

    private void OnFavoriteClick(object sender, RoutedEventArgs e)
    {
        if (ViewModel is null) return;
        var env = Towertail.WinUI.App.Current.Environment;
        env.Nodes.SetFavorite(ViewModel.Node.Id, !ViewModel.Node.Favorite);
        // Node is a record, so SetFavorite swapped the reference inside NodeStore.
        // Refresh our VM's Node pointer and icon.
        var fresh = env.Nodes.ById(ViewModel.Node.Id);
        if (fresh != null) ViewModel.UpdateNode(fresh);
        UpdateFavoriteIcon();
    }
}
