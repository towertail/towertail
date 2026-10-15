using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;

namespace Towertail.WinUI.Preferences;

public sealed partial class NotificationsPane : UserControl
{
    private readonly AppEnvironment _env;
    public NotificationsPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        EnabledSwitch.IsOn = env.ServerSettings.NotificationsEnabled;
        WarnSwitch.IsOn = env.ServerSettings.NotifyWarn;
        CriticalSwitch.IsOn = env.ServerSettings.NotifyCritical;
        DebounceBox.Value = env.ServerSettings.NotifyDebounceSeconds;
    }

    private void OnEnabledChanged(object s, Microsoft.UI.Xaml.RoutedEventArgs e)
    { _env.ServerSettings.NotificationsEnabled = EnabledSwitch.IsOn; _env.ServerSettings.Persist(); }
    private void OnWarnChanged(object s, Microsoft.UI.Xaml.RoutedEventArgs e)
    { _env.ServerSettings.NotifyWarn = WarnSwitch.IsOn; _env.ServerSettings.Persist(); }
    private void OnCriticalChanged(object s, Microsoft.UI.Xaml.RoutedEventArgs e)
    { _env.ServerSettings.NotifyCritical = CriticalSwitch.IsOn; _env.ServerSettings.Persist(); }
    private void OnDebounceChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        if (double.IsNaN(e.NewValue)) return;
        _env.ServerSettings.NotifyDebounceSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }
}
