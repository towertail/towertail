using Microsoft.UI.Xaml.Controls;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class ReviewGridPage : Page
{
    public ReviewGridPage() { InitializeComponent(); }
    public ReviewGridPage(BulkImportState state) : this()
    {
        Items.ItemsSource = state.Candidates;
    }
}
