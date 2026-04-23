using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class ProcessTable : UserControl
{
    private ServerViewModel? _vm;

    public ProcessTable() { InitializeComponent(); }

    public async void Bind(ServerViewModel vm)
    {
        _vm = vm;
        var env = Towertail.WinUI.App.Current.Environment;
        await env.Servers.EnsureProcsHydratedAsync(vm.Node.Id);
        Refresh();
        // Redraw on every new VM property event — Procs append happens inside Ingest which raises
        // ObservableObject.PropertyChanged for LastSeen, so using that as a cheap tick works.
        vm.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(ServerViewModel.LastSeen))
                DispatcherQueue.TryEnqueue(Refresh);
        };
    }

    private void Refresh()
    {
        if (_vm is null) return;
        var latest = _vm.Procs.Latest;
        Items.ItemsSource = latest?.Items ?? Array.Empty<ProcSample>();
    }
}
