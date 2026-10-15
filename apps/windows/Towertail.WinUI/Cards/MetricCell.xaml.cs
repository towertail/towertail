using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cards;

public sealed partial class MetricCell : UserControl
{
    public string Label
    {
        get => LabelText.Text;
        set => LabelText.Text = value;
    }

    private double? _value;
    public double? Value
    {
        get => _value;
        set { _value = value; UpdateText(); }
    }

    public string Format { get; set; } = "pct";

    private MetricSeries? _series;
    public MetricSeries? Series
    {
        get => _series;
        set { _series = value; Sparkline.Series = value; }
    }

    public MetricCell() { InitializeComponent(); }

    private void UpdateText()
    {
        if (_value is null)
        {
            ValueText.Text = "—";
            return;
        }
        ValueText.Text = Format switch
        {
            "pct" => $"{_value.Value:0.#}%",
            "mbps" => $"{_value.Value:0.0}",
            _ => $"{_value.Value:0.##}",
        };
    }
}
