using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cards;

public sealed partial class DiskBars : UserControl
{
    public DiskBars() { InitializeComponent(); }

    public void Bind(DiskSeries series)
    {
        Items.ItemsSource = series.Mounts;
    }
}
