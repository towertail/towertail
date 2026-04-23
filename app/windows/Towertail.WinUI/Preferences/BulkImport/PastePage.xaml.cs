using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class PastePage : Page
{
    private readonly BulkImportState _state;
    public PastePage() { InitializeComponent(); _state = new(); }
    public PastePage(BulkImportState state) : this() { _state = state; }

    private void OnChanged(object sender, TextChangedEventArgs e)
    {
        _state.Candidates = new();
        foreach (var raw in InputBox.Text.Split('\n'))
        {
            var line = raw.Trim();
            if (string.IsNullOrEmpty(line)) continue;
            var at = line.IndexOf('@');
            if (at <= 0) continue;
            var user = line[..at];
            var host = line[(at + 1)..];
            _state.Candidates.Add(new Node
            {
                DisplayName = host,
                Kind = NodeKind.Ssh,
                SshUser = user,
                SshHost = host,
            });
        }
    }
}
