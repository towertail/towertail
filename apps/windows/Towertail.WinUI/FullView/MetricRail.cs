using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;
using Windows.UI;

namespace Towertail.WinUI.FullView;

/// <summary>What one tile of the rail shows.</summary>
public sealed record RailTile(
    RailMetric Metric, string Value, Color? ValueColor, string? Value2, Color? Value2Color, string Sub,
    IReadOnlyList<ChartSeries> Lines, double? SparkMax, DateTime? CursorAt, (DateTime From, DateTime To) Window);

/// <summary>The left column of the Overview tab: seven metric tiles with a sparkline each.</summary>
public sealed class MetricRail : UserControl
{
    public const double TileWidth = 214;
    private const double SparkWidth = TileWidth - 26;
    private const double SparkHeight = 26;

    private sealed class Tile
    {
        public required Border Root;
        public required TextBlock Label, Value, Value2, Sub;
        public required Canvas Spark;
    }

    private readonly Dictionary<RailMetric, Tile> _tiles = new();
    private RailMetric _selected;

    public event Action<RailMetric>? Selected;

    public MetricRail()
    {
        var stack = new StackPanel { Spacing = 6, Width = TileWidth };
        foreach (var m in Enum.GetValues<RailMetric>())
        {
            var label = WB.Text(m.Title().ToUpperInvariant(), 11, true, WB.Muted);
            label.CharacterSpacing = 80;
            var value = WB.Text("—", 15, true, null, true);
            var value2 = WB.Text("", 15, true, null, true);
            var values = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, HorizontalAlignment = HorizontalAlignment.Right };
            values.Children.Add(value);
            values.Children.Add(value2);
            var sub = WB.Text("", 11, true, WB.Muted);
            var spark = new Canvas { Width = SparkWidth, Height = SparkHeight, HorizontalAlignment = HorizontalAlignment.Left };
            var grid = new Grid { RowSpacing = 2 };
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            Grid.SetColumn(values, 1);
            Grid.SetRow(sub, 1);
            Grid.SetColumnSpan(sub, 2);
            Grid.SetRow(spark, 2);
            Grid.SetColumnSpan(spark, 2);
            grid.Children.Add(label);
            grid.Children.Add(values);
            grid.Children.Add(sub);
            grid.Children.Add(spark);
            var root = new Border
            {
                Child = grid,
                Padding = new Thickness(12, 8, 12, 8),
                CornerRadius = new CornerRadius(10),
                BorderThickness = new Thickness(1),
                BorderBrush = WB.Stroke,
                Background = WB.Surface,
            };
            var metric = m;
            root.Tapped += (_, _) => Selected?.Invoke(metric);
            _tiles[m] = new Tile { Root = root, Label = label, Value = value, Value2 = value2, Sub = sub, Spark = spark };
            stack.Children.Add(root);
        }
        Content = new ScrollViewer { Content = stack, VerticalScrollBarVisibility = ScrollBarVisibility.Auto, Width = TileWidth + 8 };
    }

    public void Select(RailMetric m)
    {
        _selected = m;
        foreach (var (metric, tile) in _tiles)
        {
            var on = metric == m;
            tile.Root.BorderBrush = on ? WB.Fg : WB.Stroke;
            tile.Root.BorderThickness = new Thickness(on ? 1.5 : 1);
        }
    }

    public void Update(IEnumerable<RailTile> data)
    {
        var dark = WB.IsDark(this);
        foreach (var d in data)
        {
            var t = _tiles[d.Metric];
            t.Value.Text = d.Value;
            t.Value.Foreground = d.ValueColor is { } c ? WB.Brush(c) : WB.Fg;
            t.Value2.Text = d.Value2 ?? "";
            t.Value2.Foreground = d.Value2Color is { } c2 ? WB.Brush(c2) : WB.Fg;
            t.Sub.Text = d.Sub;
            DrawSpark(t.Spark, d);
        }
        Select(_selected);
    }

    private static void DrawSpark(Canvas canvas, RailTile d)
    {
        canvas.Children.Clear();
        var span = (d.Window.To - d.Window.From).TotalSeconds;
        if (span <= 0) return;
        var all = d.Lines.SelectMany(l => l.Points).Select(p => p.Value).DefaultIfEmpty(0).Max();
        var max = d.SparkMax ?? (all * 1.1 is var m && m > 0 ? m : 1);
        for (var i = 0; i < d.Lines.Count; i++)
        {
            var pts = new PointCollection();
            var src = d.Lines[i].Points;
            var stride = Math.Max(1, src.Count / 90);
            for (var k = 0; k < src.Count; k += stride)
            {
                var p = src[k];
                var x = (p.T - d.Window.From).TotalSeconds / span * SparkWidth;
                var y = SparkHeight - 1 - Math.Clamp(p.Value / max, 0, 1) * (SparkHeight - 3);
                pts.Add(new global::Windows.Foundation.Point(Math.Clamp(x, 0, SparkWidth), y));
            }
            if (pts.Count < 2) continue;
            var color = d.Lines[i].Color;
            if (i == 0)
            {
                var fill = new PointCollection();
                foreach (var p in pts) fill.Add(p);
                fill.Add(new global::Windows.Foundation.Point(pts[^1].X, SparkHeight));
                fill.Add(new global::Windows.Foundation.Point(pts[0].X, SparkHeight));
                canvas.Children.Add(new Polygon { Points = fill, Fill = WB.Brush(Color.FromArgb(0x22, color.R, color.G, color.B)) });
            }
            canvas.Children.Add(new Polyline { Points = pts, Stroke = WB.Brush(color), StrokeThickness = 1.3, StrokeLineJoin = PenLineJoin.Round });
        }
        if (d.CursorAt is { } at && at >= d.Window.From && at <= d.Window.To)
        {
            var x = (at - d.Window.From).TotalSeconds / span * SparkWidth;
            canvas.Children.Add(new Rectangle { Width = 1, Height = SparkHeight, Fill = WB.Muted, Margin = new Thickness(x, 0, 0, 0) });
        }
    }
}
