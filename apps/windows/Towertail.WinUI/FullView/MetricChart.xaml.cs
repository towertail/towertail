using LiveChartsCore;
using LiveChartsCore.Defaults;
using LiveChartsCore.SkiaSharpView;
using LiveChartsCore.SkiaSharpView.Drawing;
using LiveChartsCore.SkiaSharpView.Painting;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using SkiaSharp;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class MetricChart : UserControl
{
    private MetricSeries? _series;
    private bool _isPercent = true;
    private bool _paused;
    private readonly global::System.Collections.ObjectModel.ObservableCollection<DateTimePoint> _values = new();
    private ISeries[]? _seriesArr;

    public MetricChart()
    {
        InitializeComponent();
        // Disable animations so new points don't re-animate the whole chart from y=0 every tick.
        Chart.AnimationsSpeed = TimeSpan.Zero;
        Chart.EasingFunction = null;
        ActualThemeChanged += (_, _) => ApplyStyling();
    }

    public void Bind(MetricSeries series, string label, bool isPercent = true)
    {
        _series = series;
        _isPercent = isPercent;
        TitleText.Text = label;
        ApplyStyling();
        BuildSeriesOnce();
        RefillValues();
        _series.Points.CollectionChanged += (_, _) => DispatcherQueue.TryEnqueue(OnPointsChanged);
    }

    public void SetPaused(bool paused)
    {
        _paused = paused;
        if (!paused) RefillValues();
    }

    private SKColor AxisColor => ActualTheme == ElementTheme.Dark
        ? new SKColor(0xFF, 0xFF, 0xFF, 0x99)
        : new SKColor(0x00, 0x00, 0x00, 0x99);

    private SKColor SeparatorColor => ActualTheme == ElementTheme.Dark
        ? new SKColor(0xFF, 0xFF, 0xFF, 0x18)
        : new SKColor(0x00, 0x00, 0x00, 0x14);

    private SKColor LineColor => ActualTheme == ElementTheme.Dark
        ? new SKColor(0x60, 0xCD, 0xFF)
        : new SKColor(0x00, 0x5F, 0xB8);

    private void ApplyStyling()
    {
        var xAxis = new Axis
        {
            Labeler = v =>
            {
                try { return new System.DateTime((long)v).ToLocalTime().ToString("HH:mm"); }
                catch { return string.Empty; }
            },
            TextSize = 11,
            MinStep = System.TimeSpan.FromMinutes(1).Ticks,
            LabelsPaint = new SolidColorPaint(AxisColor),
            SeparatorsPaint = null,
            AnimationsSpeed = TimeSpan.Zero,
        };

        var yAxis = new Axis
        {
            TextSize = 11,
            LabelsPaint = new SolidColorPaint(AxisColor),
            SeparatorsPaint = new SolidColorPaint(SeparatorColor) { StrokeThickness = 1 },
            AnimationsSpeed = TimeSpan.Zero,
        };

        if (_isPercent)
        {
            yAxis.MinLimit = 0;
            yAxis.MaxLimit = 100;
            yAxis.MinStep = 25;
            yAxis.Labeler = v => $"{v:0}%";
        }
        else
        {
            yAxis.MinLimit = 0;
            yAxis.Labeler = v => v < 1 ? $"{v:0.##}" : $"{v:0.#}";
        }

        Chart.XAxes = new[] { xAxis };
        Chart.YAxes = new[] { yAxis };
        Chart.DrawMarginFrame = new DrawMarginFrame { Stroke = null, Fill = null };
    }

    private void BuildSeriesOnce()
    {
        var line = LineColor;
        var fillTop = new SKColor(line.Red, line.Green, line.Blue, 0x55);
        var fillBot = new SKColor(line.Red, line.Green, line.Blue, 0x00);

        _seriesArr = new ISeries[]
        {
            new LineSeries<DateTimePoint>
            {
                Values = _values,
                GeometrySize = 0,
                LineSmoothness = 0.4,
                Stroke = new SolidColorPaint(line, 1.8f),
                Fill = new LinearGradientPaint(
                    new[] { fillTop, fillBot },
                    new SKPoint(0, 0), new SKPoint(0, 1)),
                AnimationsSpeed = TimeSpan.Zero,
            }
        };
        Chart.Series = _seriesArr;
    }

    private void RefillValues()
    {
        if (_series is null) return;
        _values.Clear();
        foreach (var p in _series.Points) _values.Add(new DateTimePoint(p.T, p.Value));
    }

    private void OnPointsChanged()
    {
        if (_paused || _series is null) return;
        // Append any new points at the tail; drop leading ones that no longer exist in the source
        // series so the sliding window behaves like the Mac chart (data scrolls left, no redraw).
        int srcCount = _series.Points.Count;
        int dstCount = _values.Count;

        if (srcCount == 0) { _values.Clear(); return; }

        // Align the tail: find the overlap index. Cheap path — if the source has grown by N points
        // and nothing was trimmed, just append the new tail.
        if (dstCount > 0 && srcCount >= dstCount)
        {
            var lastDstTime = _values[dstCount - 1].DateTime;
            // Assume monotonic time: find last source point at-or-before lastDstTime, append what follows.
            int i = srcCount - 1;
            while (i >= 0 && _series.Points[i].T > lastDstTime) i--;
            if (i >= 0 && _series.Points[i].T == lastDstTime)
            {
                for (int j = i + 1; j < srcCount; j++)
                    _values.Add(new DateTimePoint(_series.Points[j].T, _series.Points[j].Value));
                // Trim from front if the source series has been decimated/rolled.
                while (_values.Count > srcCount) _values.RemoveAt(0);
                return;
            }
        }

        // Fallback: the series was rebuilt (e.g. history hydration) — refill.
        RefillValues();
    }
}
