using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;

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
        var selected = state.Candidates
            .Where(r => r.Included && !r.IsDuplicate && r.IsValid())
            .Select(r => r.ToNode())
            .ToList();
        await Task.Run(() => env.Nodes.AddMany(selected));
        StatusText.Text = $"Added {selected.Count} server(s).";
        Bar.IsIndeterminate = false;
        Bar.Value = 100;
    }
}
