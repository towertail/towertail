using Microsoft.UI.Xaml;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Preferences;

public sealed partial class ImportSettingsSheet : Window
{
    private SettingsExport? _export;
    private string _path = "";
    public ImportSettingsSheet() { InitializeComponent(); Title = "Import"; }

    public void Bind(SettingsExport export, string settingsPath)
    {
        _export = export;
        _path = settingsPath;
        if (export.SourcePlatform != "windows")
            PlatformWarning.IsOpen = true;
    }

    private void OnCancel(object sender, RoutedEventArgs e) { Close(); }

    private void OnApply(object sender, RoutedEventArgs e)
    {
        if (_export is null) { Close(); return; }
        var options = new SettingsImportOptions(
            ImportGeneralCheck.IsChecked == true,
            ImportThresholdsCheck.IsChecked == true,
            ImportNotificationsCheck.IsChecked == true,
            ImportNodesCheck.IsChecked == true,
            OverwriteNodesCheck.IsChecked == true);
        SettingsTransfer.Apply(_export, options, _path);
        Close();
    }
}
