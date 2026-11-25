using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// One metric of the server editor: the warn and critical boxes and a "Use global" check.
/// The boxes show the global pair while the check is on.
/// </summary>
public sealed class ThresholdOverrideRow : UserControl
{
    private readonly CheckBox _useGlobal = new() { Content = "Use global", IsChecked = true, Width = 112, MinWidth = 0, VerticalAlignment = VerticalAlignment.Center };
    private readonly NumberBox _warn;
    private readonly NumberBox _critical;
    private ThresholdPair _global = new(0, 0);

    public ThresholdMetric Metric { get; }

    public ThresholdOverrideRow(ThresholdMetric metric, string? title = null)
    {
        Metric = metric;
        var grid = new Grid { ColumnSpacing = 8 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(120) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(112) });

        var label = new TextBlock { Text = title ?? metric.Title(), VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
        _warn = NewBox();
        _critical = NewBox();
        Grid.SetColumn(_warn, 1);
        Grid.SetColumn(_critical, 2);
        Grid.SetColumn(_useGlobal, 3);
        grid.Children.Add(label);
        grid.Children.Add(_warn);
        grid.Children.Add(_critical);
        grid.Children.Add(_useGlobal);
        HorizontalContentAlignment = HorizontalAlignment.Stretch;
        Content = grid;

        _useGlobal.Checked += (_, _) => { Show(_global); UpdateEnabled(); };
        _useGlobal.Unchecked += (_, _) => UpdateEnabled();
    }

    /// <summary>The override, or null while the row uses the global pair.</summary>
    public ThresholdPair? Value
        => _useGlobal.IsChecked == true || double.IsNaN(_warn.Value) || double.IsNaN(_critical.Value)
            ? null
            : new ThresholdPair(_warn.Value / Metric.Scale(), _critical.Value / Metric.Scale()).Clamped();

    public void Bind(ThresholdPair global, ThresholdPair? current)
    {
        _global = global;
        _useGlobal.IsChecked = current is null;
        Show(current ?? global);
        UpdateEnabled();
    }

    private NumberBox NewBox() => new()
    {
        Minimum = 1,
        Maximum = Metric.IsCount() ? 10_000_000 : 100,
        SmallChange = Metric.IsCount() ? 100 : 5,
        LargeChange = Metric.IsCount() ? 1000 : 10,
        SpinButtonPlacementMode = NumberBoxSpinButtonPlacementMode.Compact,
        HorizontalAlignment = HorizontalAlignment.Stretch,
    };

    private void Show(ThresholdPair p)
    {
        _warn.Value = Math.Round(p.Warn * Metric.Scale());
        _critical.Value = Math.Round(p.Critical * Metric.Scale());
    }

    private void UpdateEnabled() => _warn.IsEnabled = _critical.IsEnabled = _useGlobal.IsChecked != true;
}
