using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class SourcePickerPage : Page
{
    private readonly BulkImportState _state;
    private readonly Action _onPick;

    public SourcePickerPage() { InitializeComponent(); _state = new(); _onPick = () => { }; }

    public SourcePickerPage(BulkImportState state, Action onPick) : this()
    {
        _state = state;
        _onPick = onPick;
    }

    private void OnTailscale(object sender, RoutedEventArgs e)
    {
        _state.Source = BulkImportSource.Tailscale;
        _onPick();
    }

    private void OnCsv(object sender, RoutedEventArgs e)
    {
        _state.Source = BulkImportSource.Csv;
        _onPick();
    }

    private void OnPaste(object sender, RoutedEventArgs e)
    {
        _state.Source = BulkImportSource.Paste;
        _onPick();
    }
}
