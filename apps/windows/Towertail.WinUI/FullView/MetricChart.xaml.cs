using LiveChartsCore;
using LiveChartsCore.Defaults;
using LiveChartsCore.Drawing;
using LiveChartsCore.Measure;
using LiveChartsCore.SkiaSharpView;
using LiveChartsCore.SkiaSharpView.Painting;
using LiveChartsCore.SkiaSharpView.Painting.Effects;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using SkiaSharp;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// A line chart of the workbench. It draws one or more lines, the warn and critical limits,
/// and the hover, pin and drag lines of the shared <see cref="WorkbenchCursor"/>.
/// A tap pins, a drag zooms on release.
/// </summary>
public sealed partial class MetricChart : UserControl
{
    private const double DragPixels = 5;

    private WorkbenchCursor? _cursor;
    private (DateTime From, DateTime To) _domain;
    private ChartFormat _format = new(v => v.ToString("0.##"));
    private IReadOnlyList<ChartSeries> _series = Array.Empty<ChartSeries>();
    private string _signature = "";
    private LineSeries<DateTimePoint>[] _lines = Array.Empty<LineSeries<DateTimePoint>>();
    private readonly RectangularSection _warn = new() { IsVisible = false };
    private readonly RectangularSection _critical = new() { IsVisible = false };
    private readonly RectangularSection _hover = new() { IsVisible = false };
    private readonly RectangularSection _pin = new() { IsVisible = false };
    private readonly RectangularSection _selection = new() { IsVisible = false };
    private double? _downX;
    private bool _dragging;
    private double _lastPx;

    public MetricChart()
    {
        InitializeComponent();
        Chart.AnimationsSpeed = TimeSpan.Zero;
        Chart.EasingFunction = null;
        Chart.TooltipPosition = TooltipPosition.Hidden;
        Chart.DrawMarginFrame = new DrawMarginFrame { Stroke = null, Fill = null };
        Chart.Sections = new[] { _selection, _warn, _critical, _hover, _pin };
        Chart.AddHandler(PointerMovedEvent, new PointerEventHandler(OnMoved), true);
        Chart.AddHandler(PointerPressedEvent, new PointerEventHandler(OnPressed), true);
        Chart.AddHandler(PointerReleasedEvent, new PointerEventHandler(OnReleased), true);
        Chart.AddHandler(PointerExitedEvent, new PointerEventHandler(OnExited), true);
        ActualThemeChanged += (_, _) => { _signature = ""; Redraw(); };
    }

    private bool Dark => WB.IsDark(this);

    public void Bind(IReadOnlyList<ChartSeries> series, (DateTime From, DateTime To) xDomain, ChartFormat format, WorkbenchCursor cursor)
    {
        if (!ReferenceEquals(_cursor, cursor))
        {
            if (_cursor is not null) _cursor.PropertyChanged -= OnCursorChanged;
            _cursor = cursor;
            cursor.PropertyChanged += OnCursorChanged;
            Unloaded += (_, _) => cursor.PropertyChanged -= OnCursorChanged;
        }
        _series = series;
        _domain = xDomain;
        _format = format;
        Redraw();
    }

    private void OnCursorChanged(object? sender, System.ComponentModel.PropertyChangedEventArgs e)
        => DispatcherQueue.TryEnqueue(UpdateSections);

    private static SKColor Sk(Windows.UI.Color c, byte? alpha = null) => new(c.R, c.G, c.B, alpha ?? c.A);

    private SKColor AxisColor => Dark ? new SKColor(0xFF, 0xFF, 0xFF, 0x99) : new SKColor(0, 0, 0, 0x99);
    private SKColor GridColor => Dark ? new SKColor(0xFF, 0xFF, 0xFF, 0x18) : new SKColor(0, 0, 0, 0x14);

    private void Redraw()
    {
        var span = _domain.To - _domain.From;
        var longSpan = span > TimeSpan.FromHours(30);
        Chart.XAxes = new[]
        {
            new Axis
            {
                MinLimit = _domain.From.Ticks,
                MaxLimit = _domain.To.Ticks,
                MinStep = Math.Max(TimeSpan.FromSeconds(30).Ticks, span.Ticks / 8),
                Labeler = v =>
                {
                    try
                    {
                        var t = new DateTime((long)v, DateTimeKind.Utc).ToLocalTime();
                        return longSpan ? t.ToString("MMM d") : t.ToString("HH:mm");
                    }
                    catch { return ""; }
                },
                TextSize = 11,
                LabelsPaint = new SolidColorPaint(AxisColor),
                SeparatorsPaint = null,
                AnimationsSpeed = TimeSpan.Zero,
            },
        };
        Chart.YAxes = new[]
        {
            new Axis
            {
                MinLimit = 0,
                MaxLimit = _format.Max,
                Labeler = _format.Value,
                TextSize = 11,
                Position = LiveChartsCore.Measure.AxisPosition.End,
                LabelsPaint = new SolidColorPaint(AxisColor),
                SeparatorsPaint = new SolidColorPaint(GridColor) { StrokeThickness = 1 },
                AnimationsSpeed = TimeSpan.Zero,
            },
        };

        var signature = string.Join("|", _series.Select(s => s.Name + s.Color)) + Dark;
        if (signature != _signature)
        {
            _signature = signature;
            _lines = _series.Select((s, i) =>
            {
                var c = Sk(s.Color);
                return new LineSeries<DateTimePoint>
                {
                    Values = Array.Empty<DateTimePoint>(),
                    GeometrySize = 0,
                    GeometryStroke = null,
                    GeometryFill = null,
                    LineSmoothness = 0,
                    Stroke = new SolidColorPaint(c, 1.8f),
                    Fill = i == 0 ? new LinearGradientPaint(new[] { Sk(s.Color, 0x44), Sk(s.Color, 0x00) }, new SKPoint(0, 0), new SKPoint(0, 1)) : null,
                    AnimationsSpeed = TimeSpan.Zero,
                    Name = s.Name,
                };
            }).ToArray();
            Chart.Series = _lines;
        }
        for (var i = 0; i < _lines.Length; i++)
            _lines[i].Values = _series[i].Points.Select(p => new DateTimePoint(p.T, p.Value)).ToArray();

        Limit(_warn, _format.Warn, WB.Warn(Dark));
        Limit(_critical, _format.Critical, WB.Critical(Dark));
        UpdateSections();
    }

    private static void Limit(RectangularSection s, double? y, Windows.UI.Color color)
    {
        s.IsVisible = y is not null;
        if (y is not { } v) return;
        s.Yi = v;
        s.Yj = v;
        s.Stroke = new SolidColorPaint(Sk(color, 0xB0), 1.2f) { PathEffect = new DashEffect(new float[] { 5, 4 }) };
    }

    private void UpdateSections()
    {
        if (_cursor is null) return;
        var dark = Dark;
        Vertical(_hover, _cursor.HoverAt, new SolidColorPaint(Sk(dark ? Windows.UI.Color.FromArgb(0xFF, 0xFF, 0xFF, 0xFF) : Windows.UI.Color.FromArgb(0xFF, 0, 0, 0), 0x60), 1f));
        Vertical(_pin, _cursor.PinnedAt, new SolidColorPaint(Sk(WB.Pin(dark)), 1.6f));
        if (_cursor.PendingSelection is { } s)
        {
            _selection.IsVisible = true;
            _selection.Xi = s.From.Ticks;
            _selection.Xj = s.To.Ticks;
            _selection.Fill = new SolidColorPaint(Sk(WB.Pin(dark), 0x22));
        }
        else _selection.IsVisible = false;
        UpdateTip();
    }

    private void Vertical(RectangularSection s, DateTime? t, SolidColorPaint paint)
    {
        var visible = t is { } v && v >= _domain.From && v <= _domain.To;
        s.IsVisible = visible;
        if (!visible) return;
        s.Xi = t!.Value.Ticks;
        s.Xj = t.Value.Ticks;
        s.Stroke = paint;
    }

    private void UpdateTip()
    {
        if (_cursor?.HoverAt is not { } t || _dragging || _series.Count == 0 || t < _domain.From || t > _domain.To)
        {
            Tip.Visibility = Visibility.Collapsed;
            return;
        }
        var parts = _series.Select(s => (SeriesMath.ValueAt(s.Points, t) is double v ? _format.Value(v) : "—"));
        TipText.Text = t.ToLocalTime().ToString("HH:mm:ss") + "  " + string.Join("  ", _series.Zip(parts, (s, v) => _series.Count > 1 ? $"{s.Name} {v}" : v));
        Tip.Visibility = Visibility.Visible;
        Tip.Measure(new global::Windows.Foundation.Size(double.PositiveInfinity, double.PositiveInfinity));
        var w = Tip.DesiredSize.Width;
        var x = Math.Clamp(_lastPx + 12, 0, Math.Max(0, ActualWidth - w - 4));
        Tip.Margin = new Thickness(x, 4, 0, 0);
    }

    private DateTime TimeAt(double px)
    {
        var ticks = Chart.ScalePixelsToData(new LvcPointD(px, 0)).X;
        ticks = Math.Clamp(ticks, _domain.From.Ticks, _domain.To.Ticks);
        return new DateTime((long)ticks, DateTimeKind.Utc);
    }

    private void OnMoved(object sender, PointerRoutedEventArgs e)
    {
        if (_cursor is null) return;
        var x = e.GetCurrentPoint(Chart).Position.X;
        _lastPx = x;
        var t = TimeAt(x);
        if (_downX is { } d && (_dragging || Math.Abs(x - d) > DragPixels))
        {
            if (!_dragging)
            {
                _dragging = true;
                _cursor.HoverAt = null;
                _cursor.BeginSelection(TimeAt(d));
            }
            _cursor.UpdateSelection(t);
            return;
        }
        _cursor.HoverAt = t;
    }

    private void OnPressed(object sender, PointerRoutedEventArgs e)
    {
        _downX = e.GetCurrentPoint(Chart).Position.X;
        _dragging = false;
    }

    private void OnReleased(object sender, PointerRoutedEventArgs e)
    {
        if (_cursor is null || _downX is null) return;
        var x = e.GetCurrentPoint(Chart).Position.X;
        _downX = null;
        if (_dragging)
        {
            _dragging = false;
            _cursor.EndSelection();
            return;
        }
        _cursor.PinnedAt = TimeAt(x);
    }

    private void OnExited(object sender, PointerRoutedEventArgs e)
    {
        if (_cursor is null) return;
        if (!_dragging) _cursor.HoverAt = null;
    }
}
