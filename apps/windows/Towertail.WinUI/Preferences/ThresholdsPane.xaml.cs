using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ThresholdsPane : UserControl
{
    private readonly AppEnvironment _env;
    private readonly (ToggleButton Box, AlertSignal Signal)[] _mutes;
    private readonly List<HealthLimitRow> _limitRows = new();

    private sealed record HealthLimitRow(
        double Scale,
        Func<HealthLimits, ThresholdPair> Get, Func<HealthLimits, ThresholdPair, HealthLimits> Set,
        NumberBox Warn, NumberBox Critical);

    public ThresholdsPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        _mutes = new (ToggleButton, AlertSignal)[]
        {
            (CpuMute, AlertSignal.Cpu), (MemMute, AlertSignal.Mem), (DiskMute, AlertSignal.Disk),
            (ProcsMute, AlertSignal.Procs), (ZombiesMute, AlertSignal.Zombies),
            (PidsMute, AlertSignal.Pids), (FilesMute, AlertSignal.Files), (InodesMute, AlertSignal.Inodes),
            (MemPressureMute, AlertSignal.MemPressure), (IoPressureMute, AlertSignal.IoPressure),
        };
        AddLimitRow("PID use", "\uE9F5", 100, l => l.Pids, (l, p) => l with { Pids = p });
        AddLimitRow("Open files", "\uE8A5", 100, l => l.Files, (l, p) => l with { Files = p });
        AddLimitRow("Inodes", "\uEDA2", 100, l => l.Inodes, (l, p) => l with { Inodes = p });
        AddLimitRow("Memory pressure · Linux", "\uEEA0", 1, l => l.MemPressure, (l, p) => l with { MemPressure = p });
        AddLimitRow("Memory pressure · macOS", "\uEEA0", 1, l => l.MacMemPressure, (l, p) => l with { MacMemPressure = p });
        AddLimitRow("I/O pressure", "\uE950", 1, l => l.IoPressure, (l, p) => l with { IoPressure = p });
        var remote = env.Backend is RemoteBackend;
        foreach (var (box, signal) in _mutes)
        {
            box.IsChecked = env.ServerSettings.MutedSignals.Contains(signal);
            box.IsEnabled = !remote;
            if (box is not CheckBox) UpdateBell(box);
        }
        if (remote) MuteNote.Text = "Mute: Not supported by this server.";
        Show(env.ServerSettings.Thresholds, env.ServerSettings.Alerts);
    }

    private void AddLimitRow(string title, string glyph, double scale,
        Func<HealthLimits, ThresholdPair> get, Func<HealthLimits, ThresholdPair, HealthLimits> set)
    {
        var style = (Style)Resources["ThresholdNumberBox"];
        var caption = (Style)Application.Current.Resources["CaptionTextBlockStyle"];
        var secondary = (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"];
        var warn = new NumberBox { Style = style, Width = 130 };
        var critical = new NumberBox { Style = style, Width = 130 };

        var grid = new Grid { Style = (Style)Resources["SettingsRowGrid"], ColumnSpacing = 16 };
        foreach (var w in new[] { GridLength.Auto, new GridLength(1, GridUnitType.Star), GridLength.Auto, GridLength.Auto, new GridLength(36) })
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = w });
        grid.Children.Add(new FontIcon { Glyph = glyph, FontSize = 18, VerticalAlignment = VerticalAlignment.Center });
        grid.Children.Add(Placed(new TextBlock
        {
            Text = title, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis,
            Style = (Style)Application.Current.Resources["BodyStrongTextBlockStyle"],
        }, 1));
        grid.Children.Add(Placed(LabeledBox("Warn %", warn, caption, secondary, 0), 2));
        grid.Children.Add(Placed(LabeledBox("Critical %", critical, caption, secondary, 16), 3));

        HealthLimitRows.Children.Add(new Border { Style = (Style)Resources["SettingsRowBorder"], Child = grid });
        _limitRows.Add(new HealthLimitRow(scale, get, set, warn, critical));
    }

    private static StackPanel LabeledBox(string label, NumberBox box, Style caption, Brush secondary, double left)
    {
        var panel = new StackPanel
        {
            Orientation = Orientation.Horizontal, Spacing = 8,
            VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(left, 0, 0, 0),
        };
        panel.Children.Add(new TextBlock { Text = label, VerticalAlignment = VerticalAlignment.Center, Style = caption, Foreground = secondary });
        panel.Children.Add(box);
        return panel;
    }

    private static T Placed<T>(T element, int column) where T : FrameworkElement
    {
        Grid.SetColumn(element, column);
        return element;
    }

    private void OnMuteToggled(object sender, RoutedEventArgs e)
    {
        if (sender is ToggleButton b and not CheckBox) UpdateBell(b);
    }

    private static void UpdateBell(ToggleButton b)
    {
        var muted = b.IsChecked == true;
        b.Content = new FontIcon { Glyph = muted ? "\uE7ED" : "\uEA8F", FontSize = 16 };
        ToolTipService.SetToolTip(b, muted ? "Muted on all servers. Click to unmute." : "Mute notifications on all servers");
    }

    private MutedSignals SelectedMutes()
        => new(_mutes.Where(m => m.Box.IsChecked == true).Select(m => m.Signal));

    private void Show(MetricThresholds t, AlertRules alerts)
    {
        CpuWarnBox.Value  = ToPct(t.CpuWarn);     CpuCritBox.Value  = ToPct(t.CpuCritical);
        MemWarnBox.Value  = ToPct(t.MemWarn);     MemCritBox.Value  = ToPct(t.MemCritical);
        DiskWarnBox.Value = ToPct(t.DiskWarn);    DiskCritBox.Value = ToPct(t.DiskCritical);
        ProcsWarnBox.Value   = t.ProcsWarn;   ProcsCritBox.Value   = t.ProcsCritical;
        ZombiesWarnBox.Value = t.ZombiesWarn; ZombiesCritBox.Value = t.ZombiesCritical;
        var limits = t.HealthOrDefault;
        foreach (var row in _limitRows)
        {
            var pair = row.Get(limits);
            row.Warn.Value = Math.Round(pair.Warn * row.Scale);
            row.Critical.Value = Math.Round(pair.Critical * row.Scale);
        }
        AlertsEditor.Rules = alerts;
    }

    private HealthLimits ReadLimits()
    {
        var limits = _env.ServerSettings.Thresholds.HealthOrDefault;
        foreach (var row in _limitRows)
        {
            if (double.IsNaN(row.Warn.Value) || double.IsNaN(row.Critical.Value)) continue;
            limits = row.Set(limits, new ThresholdPair(row.Warn.Value / row.Scale, row.Critical.Value / row.Scale).Clamped());
        }
        return limits;
    }

    private void OnResetClick(object sender, RoutedEventArgs e)
        => Show(MetricThresholds.Defaults, AlertRules.Defaults);

    private void OnSaveClick(object sender, RoutedEventArgs e)
    {
        _env.ServerSettings.Thresholds = new MetricThresholds(
            FromPct(CpuWarnBox.Value),  FromPct(CpuCritBox.Value),
            FromPct(MemWarnBox.Value),  FromPct(MemCritBox.Value),
            FromPct(DiskWarnBox.Value), FromPct(DiskCritBox.Value))
            .WithHealth(
                FromCount(ProcsWarnBox.Value, MetricThresholds.DefaultProcsWarn),
                FromCount(ProcsCritBox.Value, MetricThresholds.DefaultProcsCritical),
                FromCount(ZombiesWarnBox.Value, MetricThresholds.DefaultZombiesWarn),
                FromCount(ZombiesCritBox.Value, MetricThresholds.DefaultZombiesCritical))
            with { Health = ReadLimits() };
        _env.ServerSettings.Alerts = AlertsEditor.Rules;
        _env.ServerSettings.MutedSignals = SelectedMutes();
        _env.ServerSettings.Persist();
    }

    private static int FromCount(double v, int fallback) => double.IsNaN(v) ? fallback : (int)Math.Round(v);

    private static double ToPct(double fraction) => Math.Round(fraction * 100.0);
    private static double FromPct(double percent) => Math.Clamp(percent / 100.0, 0.0, 1.0);
}
