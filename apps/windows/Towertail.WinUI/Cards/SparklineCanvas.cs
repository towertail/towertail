using Microsoft.UI.Xaml;
using SkiaSharp;
using SkiaSharp.Views.Windows;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cards;

/// <summary>
/// A 60×20 sparkline painted via SkiaSharp. Invalidates only when the bound
/// <see cref="MetricSeries.Points"/> collection changes — no frame timer. Parity with
/// the Mac Canvas-based sparkline in paint cost.
/// </summary>
public sealed class SparklineCanvas : SKXamlCanvas
{
    private MetricSeries? _series;
    private bool _attached;

    public MetricSeries? Series
    {
        get => _series;
        set
        {
            var attached = _attached;
            if (attached) Detach();
            _series = value;
            if (attached) Attach();
            Invalidate();
        }
    }

    public SparklineCanvas()
    {
        PaintSurface += OnPaint;
        // The series lives as long as the app, so listen to it only while in the tree.
        Loaded += (_, _) => { Invalidate(); Attach(); };
        Unloaded += (_, _) => { if (!IsLoaded) Detach(); };
    }

    private void Attach()
    {
        if (_attached) return;
        if (_series != null) _series.Points.CollectionChanged += OnPointsChanged;
        _attached = true;
    }

    private void Detach()
    {
        if (!_attached) return;
        if (_series != null) _series.Points.CollectionChanged -= OnPointsChanged;
        _attached = false;
    }

    private void OnPointsChanged(object? sender, System.Collections.Specialized.NotifyCollectionChangedEventArgs e)
        => DispatcherQueue.TryEnqueue(Invalidate);

    private void OnPaint(object? sender, SKPaintSurfaceEventArgs e)
    {
        var canvas = e.Surface.Canvas;
        canvas.Clear();
        var points = _series?.Points;
        if (points is null || points.Count < 2) return;

        var w = e.Info.Width;
        var h = e.Info.Height;
        double min = double.PositiveInfinity, max = double.NegativeInfinity;
        foreach (var p in points)
        {
            if (p.Value < min) min = p.Value;
            if (p.Value > max) max = p.Value;
        }
        if (double.IsInfinity(min) || double.IsInfinity(max)) return;
        if (Math.Abs(max - min) < 0.0001) { min -= 0.5; max += 0.5; }

        using var paint = new SKPaint
        {
            Color = new SKColor(ThresholdTint.Nominal.R, ThresholdTint.Nominal.G, ThresholdTint.Nominal.B),
            IsAntialias = true,
            StrokeWidth = 1.5f,
            Style = SKPaintStyle.Stroke,
        };

        var path = new SKPath();
        var count = points.Count;
        for (int i = 0; i < count; i++)
        {
            var t = (float)i / (count - 1);
            var x = t * w;
            var frac = (points[i].Value - min) / (max - min);
            var y = (float)(h - frac * h);
            if (i == 0) path.MoveTo(x, y);
            else path.LineTo(x, y);
        }
        canvas.DrawPath(path, paint);
    }
}
