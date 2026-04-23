using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;

namespace Towertail.WinUI.Preferences;

public sealed partial class PreferencesWindow : Window
{
    private readonly AppEnvironment _env;

    public PreferencesWindow()
    {
        _env = Towertail.WinUI.App.Current.Environment;
        InitializeComponent();
        Title = "Towertail Preferences";
        Nav.SelectedItem = Nav.MenuItems[0];
        ShowPane("general");
    }

    private void OnNavSelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (args.SelectedItem is NavigationViewItem item && item.Tag is string tag)
            ShowPane(tag);
    }

    private void ShowPane(string tag)
    {
        Host.Children.Clear();
        UIElement content = tag switch
        {
            "general"       => new GeneralPane(_env),
            "thresholds"    => new ThresholdsPane(_env),
            "notifications" => new NotificationsPane(_env),
            "servers"       => new ServersPane(_env),
            "logs"          => new LogsPane(),
            "about"         => new TextBlock { Text = "Towertail — 0.1.0", FontSize = 16 },
            _ => new TextBlock { Text = "—" },
        };
        Host.Children.Add(content);
    }
}
