using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class DeployProgressPage : Page
{
    public DeployProgressPage() { InitializeComponent(); }
    public DeployProgressPage(BulkImportState state, AppEnvironment env) : this()
    {
        _ = RunAsync(state, env);
    }

    private async Task RunAsync(BulkImportState state, AppEnvironment env)
    {
        await Task.Run(() => env.Nodes.AddMany(state.Candidates));
        StatusText.Text = $"Added {state.Candidates.Count} server(s).";
        Bar.IsIndeterminate = false;
        Bar.Value = 100;
    }
}
