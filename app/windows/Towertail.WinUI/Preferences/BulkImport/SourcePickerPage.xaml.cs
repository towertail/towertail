using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class SourcePickerPage : Page
{
    private readonly BulkImportState _state;
    private readonly Action _onChanged;

    public SourcePickerPage() { InitializeComponent(); _state = new(); _onChanged = () => { }; }

    public SourcePickerPage(BulkImportState state, Action onChanged) : this()
    {
        _state = state;
        _onChanged = onChanged;
        TailscaleRadio.Checked += (_, _) => { _state.Source = BulkImportSource.Tailscale; _onChanged(); };
        PasteRadio.Checked += (_, _) => { _state.Source = BulkImportSource.Paste; _onChanged(); };
    }
}
