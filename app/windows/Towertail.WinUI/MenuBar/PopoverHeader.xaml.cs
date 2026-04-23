using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Preferences;

namespace Towertail.WinUI.MenuBar;

public sealed partial class PopoverHeader : UserControl
{
    public PopoverHeader() { InitializeComponent(); }

    private PreferencesWindow? _prefs;
    private void OnPreferencesClick(object sender, RoutedEventArgs e)
    {
        _prefs ??= new PreferencesWindow();
        _prefs.Activate();
    }
}
