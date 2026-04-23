using LiveChartsCore;
using LiveChartsCore.Defaults;
using LiveChartsCore.SkiaSharpView;
using LiveChartsCore.SkiaSharpView.Painting;
using Microsoft.UI.Xaml.Controls;
using SkiaSharp;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class MetricChart : UserControl
{
    private MetricSeries? _series;
    private readonly global::System.Collections.ObjectModel.ObservableCollection<DateTimePoint> _values = new();

    public MetricChart() { InitializeComponent(); }

    public void Bind(MetricSeries series, string label)
    {
        _series = series;
        TitleText.Text = label;
        ApplySeries();
        _series.Points.CollectionChanged += (_, _) => DispatcherQueue.TryEnqueue(ApplySeries);
    }

    private void ApplySeries()
    {
        if (_series is null) return;
        _values.Clear();
        foreach (var p in _series.Points) _values.Add(new DateTimePoint(p.T, p.Value));

        Chart.Series = new ISeries[]
        {
            new LineSeries<DateTimePoint>
            {
                Values = _values,
                GeometrySize = 0,
                Stroke = new SolidColorPaint(SKColors.SteelBlue, 1.5f),
                Fill = null,
            }
        };
    }
}

