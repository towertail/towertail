using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.State;

public enum CardDensity { A, B }

/// <summary>
/// Local-only client preferences. These never live on a Towertail server — they describe
/// how *this* PC renders the app (density, terminal choice, launch-at-startup).
/// </summary>
public sealed partial class ClientSettings : ObservableObject
{
    [ObservableProperty] private CardDensity _cardDensity;
    [ObservableProperty] private bool _launchAtStartup;
    [ObservableProperty] private string _defaultTerminalApp = "WindowsTerminal";

    private readonly string _path;

    public ClientSettings(string path)
    {
        _path = path;
        var p = SettingsPersistence.Load(path);
        _cardDensity = p.CardDensity == "b" ? CardDensity.B : CardDensity.A;
        _launchAtStartup = p.Platform.Windows.LaunchAtStartup;
        _defaultTerminalApp = p.Platform.Windows.DefaultTerminalApp;
    }

    public void ReloadFromDisk()
    {
        var p = SettingsPersistence.Load(_path);
        CardDensity = p.CardDensity == "b" ? CardDensity.B : CardDensity.A;
        LaunchAtStartup = p.Platform.Windows.LaunchAtStartup;
        DefaultTerminalApp = p.Platform.Windows.DefaultTerminalApp;
    }

    public void Persist()
    {
        var p = SettingsPersistence.Load(_path);
        p.CardDensity = CardDensity == CardDensity.B ? "b" : "a";
        p.Platform.Windows.LaunchAtStartup = LaunchAtStartup;
        p.Platform.Windows.DefaultTerminalApp = DefaultTerminalApp;
        SettingsPersistence.Save(p, _path);
    }
}
