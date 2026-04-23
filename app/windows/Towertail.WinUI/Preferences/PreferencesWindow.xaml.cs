using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Windows.Graphics;
using WinRT.Interop;

namespace Towertail.WinUI.Preferences;

public sealed partial class PreferencesWindow : Window
{
    private readonly AppEnvironment _env;

    public PreferencesWindow()
    {
        _env = Towertail.WinUI.App.Current.Environment;
        InitializeComponent();
        Title = "Towertail Preferences";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(AppTitleBar);
        ResizeInitial(880, 640);
        Nav.SelectedItem = Nav.MenuItems[0];
        ShowPane("servers");
    }

    private void ResizeInitial(int w, int h)
    {
        try
        {
            var hwnd = WindowNative.GetWindowHandle(this);
            var id = Win32Interop.GetWindowIdFromWindow(hwnd);
            AppWindow.GetFromWindowId(id)?.Resize(new SizeInt32(w, h));
        }
        catch { }
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
            "about"         => new AboutPane(),
            _ => new TextBlock { Text = "—" },
        };
        Host.Children.Add(content);
    }
}
