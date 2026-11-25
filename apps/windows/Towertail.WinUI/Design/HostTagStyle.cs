using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Design;

/// <summary>The color of a <see cref="HostTag"/>: accent for cloud, critical for offline and alerted, else muted.</summary>
public static class HostTagStyle
{
    public static Brush Brush(HostTag tag) => (Brush)Application.Current.Resources[tag switch
    {
        HostTag.Cloud => "AccentTextFillColorPrimaryBrush",
        HostTag.OfflineAlerted => "SystemFillColorCriticalBrush",
        _ => "TextFillColorSecondaryBrush",
    }];
}
