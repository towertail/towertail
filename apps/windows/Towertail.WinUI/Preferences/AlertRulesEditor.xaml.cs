using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

/// <summary>Sustain, notify, and tolerance controls for one <see cref="AlertRules"/> set.</summary>
public sealed partial class AlertRulesEditor : UserControl
{
    private static readonly int[] SustainChoices = { 0, 30, 60, 120, 300, 600, 900, 1800, 3600 };
    private static readonly (AlertNotify Value, string Label)[] NotifyChoices =
    {
        (AlertNotify.Off, "Off"), (AlertNotify.Critical, "Critical only"), (AlertNotify.All, "Warn + Critical"),
    };

    private readonly (AlertMetric Metric, ComboBox Sustain, ComboBox Notify)[] _rows;

    public AlertRulesEditor()
    {
        InitializeComponent();
        _rows = new[]
        {
            (AlertMetric.Cpu, CpuSustainBox, CpuNotifyBox),
            (AlertMetric.Mem, MemSustainBox, MemNotifyBox),
            (AlertMetric.Disk, DiskSustainBox, DiskNotifyBox),
            (AlertMetric.Health, HealthSustainBox, HealthNotifyBox),
        };
        Rules = AlertRules.Defaults;
    }

    public AlertRules Rules
    {
        get
        {
            var rules = AlertRules.Defaults;
            foreach (var (m, sustain, notify) in _rows)
            {
                var seconds = sustain.SelectedItem is ComboBoxItem { Tag: int s } ? s : AlertRules.DefaultFor(m).SustainSeconds;
                var level = notify.SelectedIndex >= 0 ? NotifyChoices[notify.SelectedIndex].Value : AlertRules.DefaultFor(m).Notify;
                rules = rules.With(m, new AlertRule(seconds, level));
            }
            var tol = double.IsNaN(ToleranceBox.Value) ? AlertRules.DefaultTolerance : ToleranceBox.Value / 100.0;
            return (rules with { Tolerance = tol }).Clamped();
        }
        set
        {
            foreach (var (m, sustain, notify) in _rows)
            {
                var rule = value.For(m);
                FillSustain(sustain, rule.SustainSeconds);
                notify.Items.Clear();
                foreach (var (_, label) in NotifyChoices) notify.Items.Add(label);
                notify.SelectedIndex = Array.FindIndex(NotifyChoices, c => c.Value == rule.Notify);
            }
            ToleranceBox.Value = Math.Round(value.Tolerance * 100);
        }
    }

    private static void FillSustain(ComboBox box, int selected)
    {
        box.Items.Clear();
        // A migrated value can fall between the presets; show it as its own entry.
        var choices = SustainChoices.Contains(selected) ? SustainChoices : SustainChoices.Append(selected).Order().ToArray();
        foreach (var s in choices)
        {
            var item = new ComboBoxItem { Content = SustainLabel(s), Tag = s };
            box.Items.Add(item);
            if (s == selected) box.SelectedItem = item;
        }
    }

    private static string SustainLabel(int seconds) => seconds switch
    {
        0 => "Immediate",
        < 60 => $"{seconds} s",
        3600 => "1 h",
        _ when seconds % 60 == 0 => $"{seconds / 60} min",
        _ => $"{seconds / 60} min {seconds % 60} s",
    };
}
