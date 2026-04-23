using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class ProcessTable : UserControl
{
    public ProcessTable() { InitializeComponent(); }

    public async void Bind(ServerViewModel vm)
    {
        // Kick off lazy hydration so the table reflects the past 2h once the tab is visited.
        var env = Towertail.WinUI.App.Current.Environment;
        await env.Servers.EnsureProcsHydratedAsync(vm.Node.Id);
        var latest = vm.Procs.Latest;
        Items.ItemsSource = latest?.Items ?? Array.Empty<ProcSample>();
    }
}
