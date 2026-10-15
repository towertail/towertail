using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Runtime.CompilerServices;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class TailscalePickerPage : Page
{
    public sealed class PeerVm : INotifyPropertyChanged
    {
        public event PropertyChangedEventHandler? PropertyChanged;
        public string HostName { get; init; } = "";
        public string DnsName { get; init; } = "";
        public IReadOnlyList<string> Tags { get; init; } = Array.Empty<string>();
        private bool _selected;
        public bool Selected
        {
            get => _selected;
            set
            {
                if (_selected == value) return;
                _selected = value;
                PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(nameof(Selected)));
                SelectionChanged?.Invoke(this, value);
            }
        }
        public event Action<PeerVm, bool>? SelectionChanged;
    }

    private readonly BulkImportState _state;
    private readonly ObservableCollection<PeerVm> _peers = new();

    public TailscalePickerPage() { InitializeComponent(); _state = new(); }
    public TailscalePickerPage(BulkImportState state) : this()
    {
        _state = state;
        Peers.ItemsSource = _peers;
        _ = LoadPeersAsync();
    }

    private void OnReload(object sender, RoutedEventArgs e) => _ = LoadPeersAsync();

    private void OnSelectAll(object sender, RoutedEventArgs e)
    {
        foreach (var p in _peers) p.Selected = true;
        UpdateCandidates();
    }

    private void OnClear(object sender, RoutedEventArgs e)
    {
        foreach (var p in _peers) p.Selected = false;
        UpdateCandidates();
    }

    private void OnPeerSelectionChanged(PeerVm _, bool __) => UpdateCandidates();

    private void UpdateCandidates()
    {
        _state.Candidates = _peers.Where(p => p.Selected).Select(p => new ImportRow
        {
            DisplayName = string.IsNullOrEmpty(p.HostName) ? p.DnsName : p.HostName,
            SshHost = p.DnsName,
            SshUser = Environment.UserName,
            Kind = NodeKind.Ssh,
            TagsText = string.Join(", ", p.Tags ?? Array.Empty<string>()),
        }).ToList();
        _state.NotifyCandidatesChanged();
    }

    private async Task LoadPeersAsync()
    {
        LoadingBar.Visibility = Visibility.Visible;
        ErrorText.Visibility = Visibility.Collapsed;
        StatusText.Text = "Loading…";
        SelectAllBtn.IsEnabled = false;
        ClearBtn.IsEnabled = false;
        _peers.Clear();
        UpdateCandidates();

        var api = new TailscaleLocalApi();
        var (status, error) = await api.TryGetStatusAsync();
        LoadingBar.Visibility = Visibility.Collapsed;

        if (error is not null)
        {
            ErrorText.Text = error;
            ErrorText.Visibility = Visibility.Visible;
            StatusText.Text = "";
            return;
        }
        if (status?.Peer is null || status.Peer.Count == 0)
        {
            StatusText.Text = "No peers in your tailnet.";
            return;
        }

        foreach (var p in status.Peer.Values.OrderBy(p => p.HostName ?? "", StringComparer.OrdinalIgnoreCase))
        {
            var vm = new PeerVm
            {
                HostName = p.HostName ?? "",
                DnsName = StripTrailingDot(p.DnsName ?? ""),
                Tags = p.Tags ?? Array.Empty<string>(),
            };
            vm.SelectionChanged += OnPeerSelectionChanged;
            _peers.Add(vm);
        }
        StatusText.Text = $"{_peers.Count} peer{(_peers.Count == 1 ? "" : "s")}";
        SelectAllBtn.IsEnabled = _peers.Count > 0;
        ClearBtn.IsEnabled = _peers.Count > 0;
    }

    private static string StripTrailingDot(string s)
        => s.EndsWith('.') ? s[..^1] : s;
}
