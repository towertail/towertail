using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>
/// Takes the data of cloud hosts from <c>/v1/stream</c> (lease mode). Hosts without a cloud
/// link stay on SSH polling. Mirrors the Mac <c>CloudFeed</c>. A frame changes the stores on
/// the UI thread through <paramref name="marshal"/>; null runs the change in place.
/// </summary>
public sealed class CloudFeed(
    RemoteClient client, NodeStore nodes, ServerStore servers, ReleasedHosts released,
    ViewLeaseController lease, Action onUnauthorized, Func<Action, Task>? marshal, Logger? log,
    ICloudAlertPoster? poster = null)
    : IDiskScanUpdates
{
    private readonly object _gate = new();
    private RemoteStream? _stream;
    private bool _started;
    private volatile bool _stopped;
    private volatile bool _connected;
    private volatile IReadOnlySet<Guid> _alerted = new HashSet<Guid>();
    // Reachability events since the last connect. They win over the open alerts, which can be older.
    private readonly Dictionary<Guid, bool> _eventAlerts = [];
    private bool _syncErrorLogged;

    public bool IsConnected => _connected;

    /// <summary>The hosts with an open reachability alert.</summary>
    public IReadOnlySet<Guid> Alerted => _alerted;

    /// <summary>Raised when <see cref="IsConnected"/> or <see cref="Alerted"/> changes.</summary>
    public event Action? StateChanged;
    public event Action? Connected;
    public event Action<StreamHello>? Hello;
    public event Action<Guid>? NodeUpdated;
    public event Action? SettingsUpdated;
    public event Action<Guid, DiskScanJob>? DiskScanUpdated;
    public event Action<StreamEvent>? EventReceived;

    public void Start()
    {
        RemoteStream stream;
        lock (_gate)
        {
            if (_started) return;
            _started = true;
            stream = _stream = client.OpenStream(lease: true, log);
        }
        stream.Connected += () => _ = OnConnectedAsync();
        stream.Disconnected += _ => OnUi(() => SetConnected(false));
        stream.MessageReceived += Handle;
        stream.Start();
    }

    public async Task StopAsync()
    {
        RemoteStream? stream;
        lock (_gate) { stream = _stream; _stream = null; _stopped = true; }
        // Without the feed no data comes, so the stale check takes over.
        var reset = OnUi(() =>
        {
            SetConnected(false);
            foreach (var vm in servers.Servers.Where(v => v.LiveSource == LiveSource.Cloud)) vm.LiveSource = LiveSource.Poll;
        });
        if (stream is not null) await stream.DisposeAsync().ConfigureAwait(false);
        await reset.ConfigureAwait(false);
    }

    /// <summary>Sends a <c>view</c> frame. The stream drops it when there is no connection.</summary>
    public Task SendAsync(LiveView view)
    {
        RemoteStream? stream;
        lock (_gate) stream = _stream;
        return stream?.SendAsync(view) ?? Task.CompletedTask;
    }

    /// <summary>
    /// Adds hosts that only the cloud knows. Links the SSH hosts that a sign-out released and
    /// that the cloud still holds. Removes the cloud link of hosts that the cloud dropped.
    /// </summary>
    public async Task SyncNodesAsync(CancellationToken ct = default)
    {
        IReadOnlyList<RemoteNode> remote;
        try { remote = await client.ListNodesAsync(ct).ConfigureAwait(false); }
        catch (RemoteException ex) when (ex.Status == 401) { onUnauthorized(); return; }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            if (!_syncErrorLogged) log?.Warning($"cloud: node list failed: {ex.Message}");
            _syncErrorLogged = true;
            return;
        }
        _syncErrorLogged = false;
        await OnUi(() => ApplySync(remote)).ConfigureAwait(false);
    }

    private void ApplySync(IReadOnlyList<RemoteNode> remote)
    {
        if (_stopped) return;
        var remoteIds = remote.Select(r => r.Id).ToHashSet();
        foreach (var r in remote.Where(r => nodes.ById(r.Id) is null))
            nodes.Add(Node.CloudOnly(r.Id, r.DisplayName) with { Cloud = new CloudLink(DateTime.UtcNow) });

        var relinked = new HashSet<Guid>();
        foreach (var r in remote.Where(r => r.Enabled && released.Contains(r.Id)))
        {
            if (nodes.ById(r.Id) is not { Kind: NodeKind.Ssh, Cloud: null } node) continue;
            nodes.Update(node with { Cloud = new CloudLink(DateTime.UtcNow) });
            relinked.Add(r.Id);
        }
        released.Retain(remoteIds.Except(relinked).ToHashSet());

        foreach (var node in nodes.Nodes.Where(n => n.IsCloud && !remoteIds.Contains(n.Id)).ToList())
        {
            log?.Info($"cloud: host removed in the cloud: {node.DisplayName}");
            if (node.Kind == NodeKind.Cloud) nodes.Remove(node.Id);
            else nodes.Update(node with { Cloud = null });
        }
    }

    internal void Handle(StreamMessage message)
    {
        if (message is StreamMessage.DiskScanUpdatedFrame scan)
        {
            DiskScanUpdated?.Invoke(scan.NodeId, scan.Job);
            return;
        }
        _ = OnUi(() => Apply(message));
    }

    private void Apply(StreamMessage message)
    {
        if (_stopped) return;
        switch (message)
        {
            case StreamMessage.SampleFrame f:
                if (IsCloudNode(f.NodeId)) servers.Ingest(f.NodeId, f.Sample);
                break;
            case StreamMessage.SummaryFrame f:
                servers.IngestSummary(f.NodeId, f.Summary);
                break;
            case StreamMessage.DetailFrame f:
                servers.ApplyDetail(f.NodeId, f.Tab, f.Detail);
                break;
            case StreamMessage.EventFrame f:
                ApplyEvent(f.Event);
                break;
            case StreamMessage.NodeUpdatedFrame f:
                ApplyNodeUpdated(f.Node);
                break;
            case StreamMessage.AlertUpdatedFrame f:
                if (f.Alert.IsSilenced(DateTimeOffset.UtcNow)) poster?.Withdraw(f.Alert.Id);
                break;
            case StreamMessage.HelloFrame f:
                // The server runs in lease mode: no data comes while no view is open.
                foreach (var vm in servers.Servers.Where(v => v.Node.IsCloud)) vm.LiveSource = LiveSource.Cloud;
                Hello?.Invoke(f.Hello);
                break;
            case StreamMessage.LeaseFrame f:
                if (f.Notice.IsLimit) lease.DidReceiveLimit();
                break;
            case StreamMessage.SettingsUpdatedFrame:
                SettingsUpdated?.Invoke();
                break;
        }
    }

    private void ApplyEvent(StreamEvent e)
    {
        EventReceived?.Invoke(e);
        // The app runs no rules for cloud hosts. It shows what the server says to show.
        if (e is { Notify: true, AlertId: not null } && nodes.ById(e.NodeId) is { IsCloud: true } host)
            poster?.Post(e, host.DisplayName);
        if (e.Metric != "reachability") return;
        servers.ApplyReachability(e);
        var open = e.Tint == "critical";
        _eventAlerts[e.NodeId] = open;
        var next = _alerted.ToHashSet();
        if (open ? next.Add(e.NodeId) : next.Remove(e.NodeId)) SetAlerted(next);
    }

    private void ApplyNodeUpdated(RemoteNode remote)
    {
        if (nodes.ById(remote.Id) is not { IsCloud: true } node) return;
        if (node.DisplayName != remote.DisplayName) nodes.Update(node with { DisplayName = remote.DisplayName });
        NodeUpdated?.Invoke(remote.Id);
    }

    private bool IsCloudNode(Guid id) => nodes.ById(id) is { IsCloud: true };

    private async Task OnConnectedAsync()
    {
        try
        {
            await OnUi(() =>
            {
                _eventAlerts.Clear();
                SetConnected(true);
            }).ConfigureAwait(false);
            lease.StreamDidConnect();
            Connected?.Invoke();
            await SyncNodesAsync().ConfigureAwait(false);
            await LoadAlertsAsync().ConfigureAwait(false);
        }
        catch (Exception ex) { log?.Warning($"cloud: connect step failed: {ex.Message}"); }
    }

    /// <summary>Seeds <see cref="Alerted"/> with the alerts that opened before the connection.</summary>
    private async Task LoadAlertsAsync()
    {
        IReadOnlyList<RemoteAlert> open;
        try { open = await client.OpenAlertsAsync().ConfigureAwait(false); }
        catch (RemoteException ex) when (ex.Status == 401) { onUnauthorized(); return; }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            log?.Warning($"cloud: alert list failed: {ex.Message}");
            return;
        }
        await OnUi(() =>
        {
            var ids = open.Where(a => a.Metric == "reachability" && a.Tint == "critical").Select(a => a.NodeId).ToHashSet();
            foreach (var (id, isOpen) in _eventAlerts)
                if (isOpen) ids.Add(id); else ids.Remove(id);
            SetAlerted(ids);
        }).ConfigureAwait(false);
    }

    private void SetConnected(bool value)
    {
        if (_connected == value) return;
        _connected = value;
        StateChanged?.Invoke();
    }

    private void SetAlerted(IReadOnlySet<Guid> value)
    {
        _alerted = value;
        StateChanged?.Invoke();
    }

    private Task OnUi(Action action)
    {
        void Safe()
        {
            try { action(); }
            catch (Exception ex) { log?.Warning($"cloud: feed step failed: {ex.Message}"); }
        }
        if (marshal is null) { Safe(); return Task.CompletedTask; }
        return marshal(Safe);
    }
}
