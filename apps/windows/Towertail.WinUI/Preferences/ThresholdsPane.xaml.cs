using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ThresholdsPane : UserControl
{
    private readonly AppEnvironment _env;
    private readonly (ToggleButton Box, AlertSignal Signal)[] _mutes;

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
        AlertsEditor.Rules = alerts;
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
                FromCount(ZombiesCritBox.Value, MetricThresholds.DefaultZombiesCritical));
        _env.ServerSettings.Alerts = AlertsEditor.Rules;
        _env.ServerSettings.MutedSignals = SelectedMutes();
        _env.ServerSettings.Persist();
    }

    private static int FromCount(double v, int fallback) => double.IsNaN(v) ? fallback : (int)Math.Round(v);

    private static double ToPct(double fraction) => Math.Round(fraction * 100.0);
    private static double FromPct(double percent) => Math.Clamp(percent / 100.0, 0.0, 1.0);
}
