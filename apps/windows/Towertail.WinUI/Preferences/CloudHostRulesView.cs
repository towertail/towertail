using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Towertail.WinUI.Backend;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// The rules of a Cloud host. The cloud holds them. Each edit goes to the cloud at once.
/// <see cref="CloudHostRulesModel"/> keeps the state. The view builds again after each change.
/// </summary>
public sealed class CloudHostRulesView : UserControl
{
    private static readonly int[] SustainChoices = { 0, 30, 60, 120, 300, 600, 900, 1800, 3600 };
    private static readonly int[] LongSustainChoices = { 60, 300, 600, 900, 1800, 3600, 7200, 21600, 86400 };
    private static readonly AlertNotify[] NotifyChoices = { AlertNotify.Off, AlertNotify.Critical, AlertNotify.All };
    private static readonly AlertNotify[] LongNotifyChoices = { AlertNotify.Off, AlertNotify.All };

    private readonly CloudHostRulesModel _model;
    private readonly StackPanel _root = new() { Spacing = 6 };
    private bool _building;

    public CloudHostRulesView(CloudHostRulesModel model)
    {
        _model = model;
        Content = _root;
        HorizontalContentAlignment = HorizontalAlignment.Stretch;
        _model.Changed += OnChanged;
        Unloaded += (_, _) => _model.Changed -= OnChanged;
        Build();
    }

    private void OnChanged() => DispatcherQueue.TryEnqueue(Build);

    private static Brush Res(string key) => (Brush)Application.Current.Resources[key];

    private bool CanEdit => _model.CanEdit;
    private RemoteHostRules Host => _model.Host;
    private RemoteOrgRules Org => _model.Org;

    private void Build()
    {
        _building = true;
        _root.Children.Clear();
        Header("Rules from the cloud");
        var info = new StackPanel { Spacing = 4 };
        info.Children.Add(Text(_model.OriginLine, secondary: true));
        if (!CanEdit) info.Children.Add(Text("Only an org admin can change the rules of a Cloud host.", secondary: true, size: 12));
        if (_model.Problem is { } p) info.Children.Add(Text(p, size: 12, brush: "SystemFillColorCriticalBrush"));
        _root.Children.Add(Card(info));

        Header("Thresholds and alert timing");
        var usage = new StackPanel();
        foreach (var m in ThresholdMetrics.Usage) usage.Children.Add(PairRow(m));
        usage.Children.Add(TimingRow("CPU alert after", AlertMetric.Cpu));
        usage.Children.Add(TimingRow("Memory alert after", AlertMetric.Mem));
        usage.Children.Add(TimingRow("Disk alert after", AlertMetric.Disk));
        usage.Children.Add(ToleranceRow());
        _root.Children.Add(Card(usage));
        Foot("A metric uses the org value until you set a value for this host.");

        Header("Host health");
        var health = new StackPanel();
        foreach (var m in ThresholdMetrics.Health) health.Children.Add(PairRow(m));
        health.Children.Add(TimingRow("Health checks alert after", AlertMetric.Health));
        _root.Children.Add(Card(health));
        Foot("A muted signal sends no notification and does not tint the tray icon.");

        Header("Long-running processes");
        _root.Children.Add(Card(LongProcRows()));
        _building = false;
    }

    // Layout helpers.

    private void Header(string title)
        => _root.Children.Add(new TextBlock { Text = title, Style = (Style)Application.Current.Resources["BodyStrongTextBlockStyle"], Margin = new Thickness(1, 12, 0, 2) });

    private void Foot(string text)
        => _root.Children.Add(new TextBlock { Text = text, FontSize = 12, TextWrapping = TextWrapping.Wrap, HorizontalAlignment = HorizontalAlignment.Right, Foreground = Res("TextFillColorSecondaryBrush") });

    private static Border Card(UIElement child) => new()
    {
        Child = child,
        Padding = new Thickness(14, 4, 14, 4),
        CornerRadius = new CornerRadius(6),
        BorderThickness = new Thickness(1),
        Background = Res("CardBackgroundFillColorDefaultBrush"),
        BorderBrush = Res("CardStrokeColorDefaultBrush"),
    };

    private static TextBlock Text(string text, bool secondary = false, double size = 14, string? brush = null) => new()
    {
        Text = text,
        FontSize = size,
        TextWrapping = TextWrapping.Wrap,
        VerticalAlignment = VerticalAlignment.Center,
        Foreground = Res(brush ?? (secondary ? "TextFillColorSecondaryBrush" : "TextFillColorPrimaryBrush")),
    };

    /// <summary>Name, source button, content and a trailing cell. Each row has a divider below.</summary>
    private static Grid Row(string name, bool dim, UIElement source, UIElement content, UIElement? trailing)
    {
        var grid = new Grid { ColumnSpacing = 12, MinHeight = 48, Padding = new Thickness(0, 6, 0, 6) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(168) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(92) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var label = Text(name, secondary: dim);
        label.TextWrapping = TextWrapping.Wrap;
        Grid.SetColumn((FrameworkElement)source, 1);
        Grid.SetColumn((FrameworkElement)content, 2);
        grid.Children.Add(label);
        grid.Children.Add(source);
        grid.Children.Add(content);
        if (trailing is not null)
        {
            Grid.SetColumn((FrameworkElement)trailing, 3);
            grid.Children.Add(trailing);
        }
        grid.BorderThickness = new Thickness(0, 0, 0, 1);
        grid.BorderBrush = Res("DividerStrokeColorDefaultBrush");
        return grid;
    }

    private Button SourceButton(bool isHost, Action<bool> set)
    {
        var b = new Button
        {
            Content = isHost ? "This host" : "Org",
            FontSize = 12,
            Padding = new Thickness(10, 3, 10, 3),
            MinWidth = 72,
            IsEnabled = CanEdit,
            VerticalAlignment = VerticalAlignment.Center,
        };
        ToolTipService.SetToolTip(b, isHost ? "Use the org value" : "Set a value for this host");
        b.Click += (_, _) => set(!isHost);
        return b;
    }

    private UIElement MuteCell(bool muted, bool inherited, Action<bool> set)
    {
        var panel = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
        if (muted) panel.Children.Add(Text("muted", size: 12, brush: "SystemFillColorCautionBrush"));
        var bell = new ToggleButton
        {
            IsChecked = muted,
            IsEnabled = _model.CanMute && !inherited,
            Width = 36, Height = 32, Padding = new Thickness(0),
            Content = new FontIcon { Glyph = muted ? "" : "", FontSize = 16 },
        };
        ToolTipService.SetToolTip(bell, inherited ? "Muted for the whole org" : muted ? "Muted. Click to unmute." : "Mute notifications for this host");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(bell, "Mute");
        bell.Click += (_, _) => set(bell.IsChecked == true);
        panel.Children.Add(bell);
        return panel;
    }

    private NumberBox Number(double value, double min, double max, Action<double> set, double width = 96)
    {
        var box = new NumberBox
        {
            Value = value, Minimum = min, Maximum = max, Width = width, IsEnabled = CanEdit,
            SpinButtonPlacementMode = NumberBoxSpinButtonPlacementMode.Compact, VerticalAlignment = VerticalAlignment.Center,
        };
        box.ValueChanged += (_, e) =>
        {
            if (_building || double.IsNaN(e.NewValue) || e.NewValue == e.OldValue) return;
            set(e.NewValue);
        };
        return box;
    }

    private ComboBox Combo<T>(IEnumerable<T> items, T selected, Func<T, string> label, Action<T> set, double width)
    {
        var box = new ComboBox { Width = width, IsEnabled = CanEdit, VerticalAlignment = VerticalAlignment.Center };
        var list = items.ToList();
        if (!list.Contains(selected)) list.Add(selected);
        foreach (var item in list) box.Items.Add(label(item));
        box.SelectedIndex = list.IndexOf(selected);
        box.SelectionChanged += (_, _) =>
        {
            if (!_building && box.SelectedIndex >= 0 && !EqualityComparer<T>.Default.Equals(list[box.SelectedIndex], selected))
                set(list[box.SelectedIndex]);
        };
        return box;
    }

    // Rows.

    private UIElement PairRow(ThresholdMetric m)
    {
        var orgPair = _model.OrgPair(m);
        var own = Host.Thresholds?.Get(m);
        var muted = _model.IsMuted(m);
        var inherited = Org.Muted.Contains(m.Signal());
        var scale = m.Scale();
        var max = m.IsCount() ? 10_000_000 : 100;
        UIElement content;
        if (own is { } pair)
        {
            var warn = Number(Math.Round(pair.Warn * scale), 1, max, v => SetPair(m, new ThresholdPair(v / scale, pair.Critical).Clamped()));
            var crit = Number(Math.Round(pair.Critical * scale), 1, max, v => SetPair(m, new ThresholdPair(pair.Warn, v / scale).Clamped()));
            var unit = m.IsCount() ? "" : "%";
            content = new StackPanel
            {
                Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center,
                Children = { warn, Text(unit, secondary: true, size: 12), crit, Text(unit, secondary: true, size: 12) },
            };
        }
        else content = Text($"{m.Format(orgPair.Warn)} / {m.Format(orgPair.Critical)}", secondary: true);
        return Row(m.Title(), muted,
            SourceButton(own is not null, toHost => _model.Edit(h => h with { Thresholds = (h.Thresholds ?? new ThresholdOverrides()).With(m, toHost ? orgPair : null) })),
            content,
            MuteCell(muted, inherited, on => _ = _model.SetMuteAsync(m.Signal(), on)));
    }

    private void SetPair(ThresholdMetric m, ThresholdPair pair)
        => _model.Edit(h => h with { Thresholds = (h.Thresholds ?? new ThresholdOverrides()).With(m, pair) });

    private UIElement TimingRow(string title, AlertMetric metric)
    {
        var orgRule = Org.Alerts.For(metric);
        var own = Host.Alerts?.Rule(metric);
        UIElement content;
        if (own is { } rule)
        {
            var sustain = Combo(SustainChoices, rule.SustainSeconds, AlertText.Sustain,
                s => SetRule(metric, rule with { SustainSeconds = s }), 130);
            var notify = Combo(NotifyChoices, rule.Notify, AlertText.Notify, n => SetRule(metric, rule with { Notify = n }), 160);
            content = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, Children = { sustain, notify } };
        }
        else content = Text($"{AlertText.Sustain(orgRule.SustainSeconds)} · {AlertText.Notify(orgRule.Notify)}", secondary: true);
        return Row(title, false,
            SourceButton(own is not null, toHost => _model.Edit(h => h with { Alerts = (h.Alerts ?? new RemoteAlertOverrides()).With(metric, toHost ? orgRule : null) })),
            content, null);
    }

    private void SetRule(AlertMetric metric, AlertRule rule)
        => _model.Edit(h => h with { Alerts = (h.Alerts ?? new RemoteAlertOverrides()).With(metric, rule) });

    private UIElement ToleranceRow()
    {
        var own = Host.Alerts?.Tolerance;
        UIElement content = own is { } t
            ? new StackPanel
            {
                Orientation = Orientation.Horizontal, Spacing = 6,
                Children =
                {
                    Number(Math.Round(t * 100), 60, 100, v => _model.Edit(h => h with { Alerts = (h.Alerts ?? new RemoteAlertOverrides()) with { Tolerance = v / 100 } })),
                    Text("% of the alert-after time", secondary: true, size: 12),
                },
            }
            : Text($"{AlertText.Percent(Org.Alerts.Tolerance)} of the alert-after time", secondary: true);
        return Row("Over the line", false,
            SourceButton(own is not null, toHost => _model.Edit(h => h with { Alerts = (h.Alerts ?? new RemoteAlertOverrides()) with { Tolerance = toHost ? Org.Alerts.Tolerance : null } })),
            content, null);
    }

    private UIElement LongProcRows()
    {
        var panel = new StackPanel();
        var own = Host.LongProc;
        var orgRule = Org.LongProc;
        var inherited = Org.Muted.Contains(AlertSignal.Long);
        UIElement content;
        if (own is { } rule)
        {
            var enabled = new ToggleSwitch { IsOn = rule.Enabled, OnContent = "On", OffContent = "Off", IsEnabled = CanEdit, MinWidth = 0 };
            enabled.Toggled += (_, _) => { if (!_building && enabled.IsOn != rule.Enabled) SetLong(rule with { Enabled = enabled.IsOn }); };
            var cpu = Number(Math.Round(rule.CpuPct), LongProcRule.MinCpuPct, LongProcRule.MaxCpuPct, v => SetLong(rule with { CpuPct = v }), 100);
            var sustain = Combo(LongSustainChoices, rule.SustainSeconds, AlertText.Sustain, s => SetLong(rule with { SustainSeconds = s }), 110);
            var notify = Combo(LongNotifyChoices, rule.Notify, AlertText.Notify, n => SetLong(rule with { Notify = n }), 150);
            content = new StackPanel
            {
                Orientation = Orientation.Horizontal, Spacing = 8, VerticalAlignment = VerticalAlignment.Center,
                Children = { enabled, cpu, Text("% CPU for", secondary: true, size: 12), sustain, notify },
            };
        }
        else content = Text(orgRule.Enabled ? $"{(int)orgRule.CpuPct}% for {AlertText.Sustain(orgRule.SustainSeconds)}" : "Off", secondary: true);
        panel.Children.Add(Row("Rule", false,
            SourceButton(own is not null, toHost => _model.Edit(h => h with { LongProc = toHost ? orgRule with { Ignore = [] } : null })),
            content,
            MuteCell(inherited || Host.Muted.Contains(AlertSignal.Long), inherited, on => _ = _model.SetMuteAsync(AlertSignal.Long, on))));
        panel.Children.Add(IgnoreRow());
        if (orgRule.Ignore.Count > 0)
            panel.Children.Add(new TextBlock
            {
                Text = $"Also ignored on all hosts: {string.Join(", ", orgRule.Ignore)}.",
                FontSize = 12, Margin = new Thickness(0, 6, 0, 8), TextWrapping = TextWrapping.Wrap, Foreground = Res("TextFillColorSecondaryBrush"),
            });
        return panel;
    }

    private void SetLong(LongProcRule rule) => _model.Edit(h => h with { LongProc = rule.Clamped() });

    private UIElement IgnoreRow()
    {
        var wrap = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
        foreach (var name in Host.IgnoredProcs)
        {
            var remove = new Button
            {
                Content = new FontIcon { Glyph = "", FontSize = 10 }, Padding = new Thickness(4), MinWidth = 0, MinHeight = 0,
                Background = Res("SubtleFillColorTransparentBrush"), BorderThickness = new Thickness(0), IsEnabled = CanEdit,
            };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, $"Remove {name}");
            var captured = name;
            remove.Click += (_, _) => _model.Edit(h => h with { IgnoredProcs = h.IgnoredProcs.Where(n => n != captured).ToList() });
            wrap.Children.Add(new Border
            {
                CornerRadius = new CornerRadius(10), Padding = new Thickness(10, 2, 2, 2), Background = Res("AccentFillColorSecondaryBrush"),
                Child = new StackPanel
                {
                    Orientation = Orientation.Horizontal, Spacing = 2,
                    Children = { new TextBlock { Text = name, FontSize = 12, VerticalAlignment = VerticalAlignment.Center, Foreground = Res("TextOnAccentFillColorPrimaryBrush") }, remove },
                },
            });
        }
        var add = new TextBox { PlaceholderText = "Add…", Width = 140, IsEnabled = CanEdit, VerticalAlignment = VerticalAlignment.Center };
        add.KeyDown += (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Enter) return;
            var name = add.Text.Trim();
            if (name.Length == 0 || Host.IgnoredProcs.Contains(name, StringComparer.OrdinalIgnoreCase)) return;
            _model.Edit(h => h with { IgnoredProcs = h.IgnoredProcs.Append(name).ToList() });
        };
        wrap.Children.Add(add);
        var row = Row("Ignore on this host", false, new Border(), wrap, null);
        Grid.SetColumn(wrap, 1);
        Grid.SetColumnSpan(wrap, 3);
        return row;
    }
}
