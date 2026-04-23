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

public sealed class CpuPctFormatConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, string language)
    {
        if (value is double v) return $"{v:0.0}";
        return "—";
    }

    public object ConvertBack(object value, Type targetType, object parameter, string language)
        => throw new NotSupportedException();
}

public sealed class BytesFormatConverter : IValueConverter
{
    public object Convert(object value, Type targetType, object parameter, string language)
    {
        long bytes = value switch
        {
            long l => l,
            int i => i,
            double d => (long)d,
            _ => 0L,
        };
        if (bytes <= 0) return "0";
        const double KB = 1024, MB = KB * 1024, GB = MB * 1024, TB = GB * 1024;
        if (bytes >= TB) return $"{bytes / TB:0.0} TB";
        if (bytes >= GB) return $"{bytes / GB:0.0} GB";
        if (bytes >= MB) return $"{bytes / MB:0.0} MB";
        if (bytes >= KB) return $"{bytes / KB:0.0} KB";
        return $"{bytes} B";
    }

    public object ConvertBack(object value, Type targetType, object parameter, string language)
        => throw new NotSupportedException();
}
