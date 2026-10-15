using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using System.ComponentModel;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class PortsTable : UserControl
{
    private ServerViewModel? _vm;
    private PropertyChangedEventHandler? _handler;
    private bool _killInFlight;

    public PortsTable() { InitializeComponent(); }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        Refresh();
        // Re-render whenever the VM signals new state. The sampler refreshes
        // ports every 10s by default, so this fires a handful of times per
        // minute — cheap to redraw fully.
        _handler = (_, args) =>
        {
            if (args.PropertyName == nameof(ServerViewModel.Ports) ||
                args.PropertyName == nameof(ServerViewModel.PortsAvailable) ||
                args.PropertyName == nameof(ServerViewModel.LastSeen))
                DispatcherQueue.TryEnqueue(Refresh);
        };
        vm.PropertyChanged += _handler;
        Unloaded += (_, _) =>
        {
            if (_handler is { } h && _vm is { } v) v.PropertyChanged -= h;
        };
    }

    private void Refresh()
    {
        if (_vm is null) return;
        var ports = _vm.Ports;
        if (ports is null)
        {
            ScopeText.Text = _vm.PortsAvailable ? "" : "ports collection disabled";
            CollectedText.Text = "";
            TruncatedText.Visibility = Visibility.Collapsed;
            Items.ItemsSource = Array.Empty<PortRow>();
            return;
        }
        ScopeText.Text = ports.Root ? "root" : "user scope";
        CollectedText.Text = ports.CollectedTs.ToLocalTime().ToString("HH:mm:ss");
        TruncatedText.Visibility = ports.Truncated ? Visibility.Visible : Visibility.Collapsed;
        var canKill = _vm.Node is not null;
        Items.ItemsSource = ports.Items.Select(p => PortRow.From(p, canKill)).ToList();
    }

    private async void OnKillRequested(object sender, PidKillEventArgs e)
    {
        if (_vm is null || _killInFlight) return;
        var node = _vm.Node;
        await KillFlow.RunAsync(this, node, e.Pid, e.Name,
            inFlight => _killInFlight = inFlight);
    }
}

/// <summary>
/// Display projection over <see cref="PortItem"/>. WinUI Bindings need
/// publicly settable strings so concatenations live here, not in XAML.
/// </summary>
public sealed record PortRow(
    int Pid,
    string Name,
    string User,
    string ListenTcpText,
    string ListenUdpText,
    int EstOut,
    int EstIn,
    string TopRemotesText,
    bool CanKill)
{
    public static PortRow From(PortItem p, bool canKill)
    {
        var tcp = (p.ListenTcp is { Count: > 0 })
            ? string.Join(", ", p.ListenTcp)
            : "—";
        var udp = (p.ListenUdp is { Count: > 0 })
            ? string.Join(", ", p.ListenUdp)
            : "—";
        var remotes = (p.TopRemotePorts is { Count: > 0 })
            ? string.Join(" ", p.TopRemotePorts.Select(r => $":{r.Port}×{r.Count}"))
            : "—";
        return new PortRow(
            p.Pid,
            p.Name ?? "—",
            p.User ?? "—",
            tcp,
            udp,
            p.EstOut,
            p.EstIn,
            remotes,
            canKill);
    }
}
