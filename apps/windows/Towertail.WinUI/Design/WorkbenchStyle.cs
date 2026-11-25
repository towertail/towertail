using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.State;
using Windows.UI;

namespace Towertail.WinUI.Design;

/// <summary>Colors and small builders of the host workbench. The colors match the Mac <c>WB</c>.</summary>
public static class WB
{
    private static Color C(uint rgb) => Color.FromArgb(0xFF, (byte)(rgb >> 16), (byte)(rgb >> 8), (byte)rgb);

    public static bool IsDark(FrameworkElement e) => e.ActualTheme == ElementTheme.Dark;

    /// <summary>The line color of a rail metric. <paramref name="line"/> 1 is the second line of net and I/O.</summary>
    public static Color Metric(RailMetric m, bool dark, int line = 0) => (m, line) switch
    {
        (RailMetric.Cpu, _) => dark ? C(0x30d158) : C(0x1f9d48),
        (RailMetric.Mem, _) => dark ? C(0x0a84ff) : C(0x0a6fd8),
        (RailMetric.Load, _) => dark ? C(0x7d7dff) : C(0x5b5bd6),
        (RailMetric.Disk, _) => dark ? C(0x40c8c8) : C(0x0e8a8a),
        (RailMetric.Net, 0) => dark ? C(0x0a84ff) : C(0x0a6fd8),
        (RailMetric.Net, _) => dark ? C(0xbf5af2) : C(0x8e44c9),
        (RailMetric.Io, 0) => dark ? C(0x40c8c8) : C(0x0e8a8a),
        (RailMetric.Io, _) => dark ? C(0xff5ca8) : C(0xc0397a),
        _ => dark ? C(0x64d2ff) : C(0x0b7fb0),
    };

    public static Color Pin(bool dark) => dark ? C(0xffd60a) : C(0x9a7b00);
    public static Color Warn(bool dark) => dark ? C(0xff9f0a) : C(0xc77700);
    public static Color Critical(bool dark) => dark ? C(0xff453a) : C(0xd70015);
    public static Color Ok(bool dark) => dark ? C(0x30d158) : C(0x1f9d48);

    public static Color Level(HealthLevel l, bool dark) => l switch
    {
        HealthLevel.Critical => Critical(dark),
        HealthLevel.Warn => Warn(dark),
        _ => Ok(dark),
    };

    public static SolidColorBrush Brush(Color c) => new(c);

    public static Brush Res(string key) => (Brush)Application.Current.Resources[key];
    public static Brush Muted => Res("TextFillColorSecondaryBrush");
    public static Brush Faint => Res("TextFillColorTertiaryBrush");
    public static Brush Fg => Res("TextFillColorPrimaryBrush");
    public static Brush Surface => Res("CardBackgroundFillColorDefaultBrush");
    public static Brush Stroke => Res("CardStrokeColorDefaultBrush");
    public static Brush Divider => Res("DividerStrokeColorDefaultBrush");
    public static FontFamily Mono => (FontFamily)Application.Current.Resources["MonoFont"];

    public static TextBlock Text(string text, double size = 12, bool mono = false, Brush? fg = null, bool bold = false)
        => new()
        {
            Text = text,
            FontSize = size,
            FontFamily = mono ? Mono : new FontFamily("Segoe UI Variable"),
            Foreground = fg ?? Fg,
            FontWeight = bold ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal,
            VerticalAlignment = VerticalAlignment.Center,
        };

    public static Border Card(UIElement child, double radius = 12, Thickness? padding = null) => new()
    {
        Background = Surface,
        BorderBrush = Stroke,
        BorderThickness = new Thickness(1),
        CornerRadius = new CornerRadius(radius),
        Padding = padding ?? new Thickness(0),
        Child = child,
    };

    /// <summary>"12 s ago" style age for the live badge.</summary>
    public static string Ago(DateTime? t)
        => t is { } v ? $"{Math.Max(0, (int)(DateTime.UtcNow - v).TotalSeconds)} s ago" : "no data";
}

/// <summary>A segmented control: one pill, one selected item, items that can be off.</summary>
public sealed class Segmented : UserControl
{
    private readonly StackPanel _row = new() { Orientation = Orientation.Horizontal, Spacing = 2 };
    private readonly List<Button> _buttons = new();
    private int _selected = -1;

    public event Action<int>? SelectionChanged;

    public Segmented(IEnumerable<string> items, bool mono = false, bool upper = false)
    {
        foreach (var (title, i) in items.Select((t, i) => (t, i)))
        {
            var b = new Button
            {
                Content = WB.Text(upper ? title.ToUpperInvariant() : title, 12, mono, null, false),
                Padding = new Thickness(12, 4, 12, 4),
                MinHeight = 0,
                MinWidth = 0,
                CornerRadius = new CornerRadius(6),
                BorderThickness = new Thickness(0),
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            };
            ((TextBlock)b.Content).Foreground = WB.Muted;
            var index = i;
            b.Click += (_, _) => Select(index, true);
            _buttons.Add(b);
            _row.Children.Add(b);
        }
        Content = new Border
        {
            Child = _row,
            Padding = new Thickness(2),
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            BorderBrush = WB.Stroke,
            Background = WB.Surface,
        };
    }

    public int Selected => _selected;

    public void Select(int index, bool notify = false)
    {
        if (index == _selected) return;
        _selected = index;
        for (var i = 0; i < _buttons.Count; i++)
        {
            var on = i == index;
            _buttons[i].Background = on ? new SolidColorBrush(Windows.UI.Color.FromArgb(0x26, 0x80, 0x80, 0x80)) : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            if (_buttons[i].Content is TextBlock t) t.Foreground = on ? WB.Fg : WB.Muted;
        }
        if (notify) SelectionChanged?.Invoke(index);
    }

    public void SetEnabled(int index, bool enabled)
    {
        if (index < _buttons.Count && _buttons[index].IsEnabled != enabled) _buttons[index].IsEnabled = enabled;
    }

}
