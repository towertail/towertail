using System.Collections.ObjectModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;

namespace Towertail.WinUI.MenuBar;

public sealed partial class PopoverRoot : UserControl
{
    public ObservableCollection<ServerViewModel> FilteredServers { get; } = new();

    private AppEnvironment? _env;
    private string _search = "";
    private ServerFilter _filter = ServerFilter.All;

    public PopoverRoot() { InitializeComponent(); }

    public void Bind(AppEnvironment env)
    {
        _env = env;
        Refresh();
        env.Servers.Servers.CollectionChanged += (_, _) => Refresh();
    }

    private void OnSearchChanged(object sender, TextChangedEventArgs e)
    {
        _search = SearchBox.Text;
        Refresh();
    }

    private void OnFilterChanged(object sender, SelectionChangedEventArgs e)
    {
        _filter = (ServerFilter)Math.Clamp(FilterBox.SelectedIndex, 0, 3);
        Refresh();
    }

    private void Refresh()
    {
        if (_env is null) return;
        FilteredServers.Clear();
        foreach (var vm in _env.Servers.Servers)
        {
            if (!MatchesSearch(vm)) continue;
            if (!MatchesFilter(vm)) continue;
            FilteredServers.Add(vm);
        }
    }

    private bool MatchesSearch(ServerViewModel vm)
    {
        if (string.IsNullOrWhiteSpace(_search)) return true;
        return vm.Node.DisplayName.Contains(_search, StringComparison.OrdinalIgnoreCase)
            || vm.Node.UserAtHost.Contains(_search, StringComparison.OrdinalIgnoreCase);
    }

    private bool MatchesFilter(ServerViewModel vm) => _filter switch
    {
        ServerFilter.All => true,
        ServerFilter.Online => vm.LastSeen != null && (DateTime.UtcNow - vm.LastSeen.Value).TotalSeconds < 30,
        ServerFilter.Warn => (vm.CpuPct ?? 0) >= 75 || (vm.MemPct ?? 0) >= 75 || (vm.DiskMaxPct ?? 0) >= 75,
        ServerFilter.Down => vm.LastSeen == null || (DateTime.UtcNow - vm.LastSeen.Value).TotalSeconds > 60,
        _ => true,
    };
}

public enum ServerFilter { All, Online, Warn, Down }
