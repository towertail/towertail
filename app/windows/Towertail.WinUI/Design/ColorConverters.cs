using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Data;
using Microsoft.UI.Xaml.Media;

namespace Towertail.WinUI.Design;

/// <summary>
/// Converts a <c>double?</c> metric value into a tint brush based on the configured thresholds.
/// </summary>
public sealed class MetricToBrushConverter : IValueConverter
{
    public double Warn { get; set; } = 0.75;
    public double Critical { get; set; } = 0.90;
    public bool IsFraction { get; set; } = true;

    public object Convert(object value, Type targetType, object parameter, string language)
    {
        if (value is not double v) return new SolidColorBrush(ThresholdTint.Muted);
        var frac = IsFraction ? v : v / 100.0;
        return new SolidColorBrush(ThresholdTint.Resolve(frac, Warn, Critical));
    }

    public object ConvertBack(object value, Type targetType, object parameter, string language)
        => throw new NotSupportedException();
}

public sealed class PercentFormatConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, string language)
    {
        if (value is double v) return $"{v:0.#}%";
        return "—";
    }

    public object ConvertBack(object value, Type targetType, object parameter, string language)
        => throw new NotSupportedException();
}

public sealed class MBpsFormatConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, string language)
    {
        if (value is double v) return $"{v:0.0}";
        return "—";
    }

    public object ConvertBack(object value, Type targetType, object parameter, string language)
        => throw new NotSupportedException();
}
