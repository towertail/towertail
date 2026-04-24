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
        LaunchAtStartupSwitch.IsOn = env.ClientSettings.LaunchAtStartup;
    }

    private void OnLocalPollChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        if (double.IsNaN(e.NewValue)) return;
        _env.ServerSettings.LocalPollingIntervalSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnSshPollChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        if (double.IsNaN(e.NewValue)) return;
        _env.ServerSettings.SshPollingIntervalSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnGraceChanged(NumberBox s, NumberBoxValueChangedEventArgs e)
    {
        if (double.IsNaN(e.NewValue)) return;
        _env.ServerSettings.PostWakeGraceSeconds = (int)e.NewValue;
        _env.ServerSettings.Persist();
    }

    private void OnLaunchChanged(object sender, Microsoft.UI.Xaml.RoutedEventArgs e)
    {
        _env.ClientSettings.LaunchAtStartup = LaunchAtStartupSwitch.IsOn;
        _env.ClientSettings.Persist();
        Towertail.WinUI.SystemServices.LaunchAtLogin.Apply(_env.ClientSettings.LaunchAtStartup);
    }
}
