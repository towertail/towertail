using Microsoft.UI;
using Microsoft.UI.Xaml.Media;
using Windows.UI;

namespace Towertail.WinUI.Design;

public static class ThresholdTint
{
    public static readonly Color Nominal = Color.FromArgb(0xFF, 0x4C, 0xAF, 0x50);
    public static readonly Color Warn    = Color.FromArgb(0xFF, 0xFF, 0xC1, 0x07);
    public static readonly Color Critical = Color.FromArgb(0xFF, 0xF4, 0x43, 0x36);

    public static SolidColorBrush Brush(Color c) => new(c);
}
