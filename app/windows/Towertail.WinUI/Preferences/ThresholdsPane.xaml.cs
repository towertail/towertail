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
        var t = env.ServerSettings.Thresholds;
        CpuWarnBox.Value  = ToPct(t.CpuWarn);     CpuCritBox.Value  = ToPct(t.CpuCritical);
        MemWarnBox.Value  = ToPct(t.MemWarn);     MemCritBox.Value  = ToPct(t.MemCritical);
        DiskWarnBox.Value = ToPct(t.DiskWarn);    DiskCritBox.Value = ToPct(t.DiskCritical);
        CpuSustainBox.Value  = t.CpuSustainSamples;
        MemSustainBox.Value  = t.MemSustainSamples;
        DiskSustainBox.Value = t.DiskSustainSamples;
    }

    private void OnSaveClick(object sender, RoutedEventArgs e)
    {
        _env.ServerSettings.Thresholds = new MetricThresholds(
            FromPct(CpuWarnBox.Value),  FromPct(CpuCritBox.Value),
            FromPct(MemWarnBox.Value),  FromPct(MemCritBox.Value),
            FromPct(DiskWarnBox.Value), FromPct(DiskCritBox.Value),
            FromSustain(CpuSustainBox.Value),
            FromSustain(MemSustainBox.Value),
            FromSustain(DiskSustainBox.Value));
        _env.ServerSettings.Persist();
    }

    private static int FromSustain(double v) => double.IsNaN(v) ? 1 : Math.Max(1, (int)Math.Round(v));

    private static double ToPct(double fraction) => Math.Round(fraction * 100.0);
    private static double FromPct(double percent) => Math.Clamp(percent / 100.0, 0.0, 1.0);
}
