using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ThresholdsPane : UserControl
{
    private readonly AppEnvironment _env;
    public ThresholdsPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        Show(env.ServerSettings.Thresholds, env.ServerSettings.Alerts);
    }

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
        _env.ServerSettings.Persist();
    }

    private static int FromCount(double v, int fallback) => double.IsNaN(v) ? fallback : (int)Math.Round(v);

    private static double ToPct(double fraction) => Math.Round(fraction * 100.0);
    private static double FromPct(double percent) => Math.Clamp(percent / 100.0, 0.0, 1.0);
}
