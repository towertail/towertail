using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Preferences;

namespace Towertail.WinUI.MenuBar;

public sealed partial class PopoverHeader : UserControl
{
    public event EventHandler? CloseRequested;

    public PopoverHeader() { InitializeComponent(); }

    private PreferencesWindow? _prefs;
    private void OnPreferencesClick(object sender, RoutedEventArgs e)
    {
        if (_prefs is null)
        {
            _prefs = new PreferencesWindow();
            _prefs.Closed += (_, _) => _prefs = null;
        }
        _prefs.Activate();
    }

    private void OnCloseClick(object sender, RoutedEventArgs e)
        => CloseRequested?.Invoke(this, EventArgs.Empty);
}
