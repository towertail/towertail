using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using System.ComponentModel;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class PortsTable : UserControl
{
    private ServerViewModel? _vm;
    private bool _attached;
    private bool _killInFlight;

    public PortsTable()
    {
        InitializeComponent();
        Loaded += (_, _) => { Refresh(); Attach(); };
        Unloaded += (_, _) => { if (!IsLoaded) Detach(); };
    }

    public void Bind(ServerViewModel vm)
    {
        if (_attached) Detach();
        _vm = vm;
        Refresh();
        if (IsLoaded) Attach();
    }

    // The view model lives as long as the app, so the table listens only while it is in the tree.
    private void Attach()
    {
        if (_attached || _vm is null) return;
        _vm.PropertyChanged += OnVmChanged;
        _attached = true;
    }

    private void Detach()
    {
        if (!_attached) return;
        _vm!.PropertyChanged -= OnVmChanged;
        _attached = false;
    }

    // The sampler refreshes ports every 10s by default, so this fires a handful of
    // times per minute — cheap to redraw fully.
    private void OnVmChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(ServerViewModel.Ports) ||
            args.PropertyName == nameof(ServerViewModel.PortsAvailable) ||
            args.PropertyName == nameof(ServerViewModel.LastSeen))
            DispatcherQueue.TryEnqueue(Refresh);
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
