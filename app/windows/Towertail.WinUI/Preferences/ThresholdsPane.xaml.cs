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
        CpuWarnBox.Value = t.CpuWarn;   CpuCritBox.Value = t.CpuCritical;
        MemWarnBox.Value = t.MemWarn;   MemCritBox.Value = t.MemCritical;
        DiskWarnBox.Value = t.DiskWarn; DiskCritBox.Value = t.DiskCritical;
    }

    private void OnSaveClick(object sender, RoutedEventArgs e)
    {
        _env.ServerSettings.Thresholds = new MetricThresholds(
            CpuWarnBox.Value, CpuCritBox.Value,
            MemWarnBox.Value, MemCritBox.Value,
            DiskWarnBox.Value, DiskCritBox.Value);
        _env.ServerSettings.Persist();
    }
}
