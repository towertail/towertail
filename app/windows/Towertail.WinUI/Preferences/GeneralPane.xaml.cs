using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;

namespace Towertail.WinUI.Preferences;

public sealed partial class GeneralPane : UserControl
{
    private readonly AppEnvironment _env;
    public GeneralPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        LocalPollBox.Value = env.ServerSettings.LocalPollingIntervalSeconds;
        SshPollBox.Value = env.ServerSettings.SshPollingIntervalSeconds;
        GraceBox.Value = env.ServerSettings.PostWakeGraceSeconds;
        LaunchAtStartupCheck.IsChecked = env.ClientSettings.LaunchAtStartup;
        AutoUpdateCheck.IsChecked = env.ServerSettings.AutoUpdateSamplersEnabled;
    }

    private void OnLocalPollChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        _env.ServerSettings.LocalPollingIntervalSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnSshPollChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        _env.ServerSettings.SshPollingIntervalSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnGraceChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        _env.ServerSettings.PostWakeGraceSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnLaunchChanged(object sender, Microsoft.UI.Xaml.RoutedEventArgs e)
    {
        _env.ClientSettings.LaunchAtStartup = LaunchAtStartupCheck.IsChecked == true;
        _env.ClientSettings.Persist();
        Towertail.WinUI.SystemServices.LaunchAtLogin.Apply(_env.ClientSettings.LaunchAtStartup);
    }

    private void OnAutoUpdateChanged(object sender, Microsoft.UI.Xaml.RoutedEventArgs e)
    {
        _env.ServerSettings.AutoUpdateSamplersEnabled = AutoUpdateCheck.IsChecked == true;
        _env.ServerSettings.Persist();
    }
}
