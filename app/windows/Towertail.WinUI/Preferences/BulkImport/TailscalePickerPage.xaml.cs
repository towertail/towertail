using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class TailscalePickerPage : Page
{
    private readonly BulkImportState _state;
    public TailscalePickerPage() { InitializeComponent(); _state = new(); }
    public TailscalePickerPage(BulkImportState state) : this()
    {
        _state = state;
        _ = LoadPeersAsync();
        Peers.SelectionChanged += (_, _) =>
        {
            _state.Candidates = Peers.SelectedItems.OfType<TailscaleLocalApi.TailscalePeer>()
                .Select(p => new Node
                {
                    DisplayName = p.HostName,
                    Kind = NodeKind.Ssh,
                    SshHost = p.DnsName,
                    Tags = p.Tags?.ToList() ?? new List<string>(),
                }).ToList();
        };
    }

    private async Task LoadPeersAsync()
    {
        var api = new TailscaleLocalApi();
        var status = await api.GetStatusAsync();
        if (status?.Peer is null) { Peers.ItemsSource = Array.Empty<object>(); return; }
        Peers.ItemsSource = status.Peer.Values.ToList();
    }
}
