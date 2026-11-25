using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>
/// Owns the cloud parts of the app: the account and, while the user is signed in, the client
/// and the feed of cloud host data. The app keeps its local backend. Mirrors the Mac <c>CloudSession</c>.
/// Implements <see cref="IDiskScanUpdates"/> for the current feed, so a scanner can hold it across sign-ins.
/// </summary>
public sealed class CloudSession : IDiskScanUpdates
{
    private readonly NodeStore _nodes;
    private readonly CloudPrefs _prefs;
    private readonly Func<Action, Task>? _marshal;
    private readonly Logger? _log;
    private readonly object _gate = new();
    private string? _feedToken;

    public CloudSession(
        CloudAccount account, NodeStore nodes, ServerStore servers, CloudPrefs prefs,
        Func<Action, Task>? marshal, Logger? log)
    {
        Account = account;
        _nodes = nodes;
        Servers = servers;
        _prefs = prefs;
        _marshal = marshal;
        _log = log;
        Released = new ReleasedHosts(prefs);
        // The lease outlives the feed, so a sign-in while the popover is open sends the view.
        Lease = new ViewLeaseController(view => Feed?.SendAsync(view) ?? Task.CompletedTask);
        account.StatusChanged += OnStatusChanged;
    }

    public CloudAccount Account { get; }
    public NodeStore Nodes => _nodes;
    public ServerStore Servers { get; }
    public ViewLeaseController Lease { get; }
    public ReleasedHosts Released { get; }

    /// <summary>Not null while the user is signed in.</summary>
    public RemoteClient? Client { get; private set; }
    public CloudFeed? Feed { get; private set; }

    public event Action<Guid, DiskScanJob>? DiskScanUpdated;

    /// <summary>
    /// Raised when the sign-in, the feed or the feed state (connected, alerted hosts) changes.
    /// It can run on any thread. A view marshals it.
    /// </summary>
    public event Action? StateChanged;

    /// <summary>The tag of a host for the cards and the server list. See <see cref="HostTags.For"/>.</summary>
    public HostTag? TagFor(Node node)
    {
        var feed = Feed;
        return HostTags.For(node, Account.IsSignedIn, feed?.IsConnected ?? false, feed?.Alerted.Contains(node.Id) ?? false);
    }

    /// <summary>The state of a host for the Servers list. See <see cref="HostStatus.For"/>.</summary>
    public HostStatus StatusFor(Node node)
    {
        var feed = Feed;
        return HostStatus.For(node, Servers.Find(node.Id), feed?.IsConnected ?? false, feed?.Alerted.Contains(node.Id) ?? false);
    }

    /// <summary>Restores the sign-in. When the user is signed in, the feed starts.</summary>
    public async Task StartAsync()
    {
        await Account.RestoreAsync().ConfigureAwait(false);
        if (Account.IsSignedIn) StartFeed();
    }

    public void StartFeed()
    {
        if (Account.Token is not { } token) return;
        CloudFeed? old;
        CloudFeed feed;
        RemoteClient? oldClient;
        lock (_gate)
        {
            if (Feed is not null && _feedToken == token) return;
            old = Feed;
            oldClient = Client;
            var client = new RemoteClient(_prefs.Endpoint, token);
            feed = new CloudFeed(client, _nodes, Servers, Released, Lease, Account.HandleUnauthorized, _marshal, _log);
            feed.DiskScanUpdated += OnDiskScanUpdated;
            feed.StateChanged += OnFeedStateChanged;
            Client = client;
            Feed = feed;
            _feedToken = token;
        }
        if (old is not null || oldClient is not null) _ = Retire(old, oldClient);
        _log?.Info("cloud: start feed");
        feed.Start();
        StateChanged?.Invoke();
    }

    public async Task StopFeedAsync()
    {
        CloudFeed? feed;
        RemoteClient? client;
        lock (_gate)
        {
            feed = Feed;
            client = Client;
            Feed = null;
            Client = null;
            _feedToken = null;
        }
        await Retire(feed, client).ConfigureAwait(false);
        StateChanged?.Invoke();
    }

    /// <summary>
    /// Stops the feed, removes the cloud-only hosts, clears the cloud link of SSH hosts and
    /// records them in <see cref="Released"/>. Then signs out of the account.
    /// </summary>
    public async Task SignOutAsync()
    {
        await StopFeedAsync().ConfigureAwait(false);
        await ReleaseHostsAsync().ConfigureAwait(false);
        await Account.SignOutAsync().ConfigureAwait(false);
    }

    private async Task Retire(CloudFeed? feed, RemoteClient? client)
    {
        try
        {
            if (feed is not null)
            {
                feed.DiskScanUpdated -= OnDiskScanUpdated;
                feed.StateChanged -= OnFeedStateChanged;
                await feed.StopAsync().ConfigureAwait(false);
            }
            if (client is not null) await client.DisposeAsync().ConfigureAwait(false);
        }
        catch (Exception ex) { _log?.Warning($"cloud: stop feed failed: {ex.Message}"); }
    }

    private void OnDiskScanUpdated(Guid node, DiskScanJob job) => DiskScanUpdated?.Invoke(node, job);

    private void OnFeedStateChanged() => StateChanged?.Invoke();

    private void OnStatusChanged(CloudStatus status)
    {
        StateChanged?.Invoke();
        switch (status)
        {
            case CloudStatus.SignedIn: StartFeed(); break;
            case CloudStatus.SignedOut: _ = StopAndReleaseAsync(); break;
        }
    }

    private async Task StopAndReleaseAsync()
    {
        await StopFeedAsync().ConfigureAwait(false);
        await ReleaseHostsAsync().ConfigureAwait(false);
    }

    private Task ReleaseHostsAsync()
    {
        void Release()
        {
            var freed = new List<Guid>();
            foreach (var node in _nodes.Nodes.Where(n => n.IsCloud).ToList())
            {
                if (node.Kind == NodeKind.Cloud) { _nodes.Remove(node.Id); continue; }
                _nodes.Update(node with { Cloud = null });
                freed.Add(node.Id);
            }
            Released.AddRange(freed);
        }
        void Safe()
        {
            try { Release(); }
            catch (Exception ex) { _log?.Warning($"cloud: release hosts failed: {ex.Message}"); }
        }
        if (_marshal is null) { Safe(); return Task.CompletedTask; }
        return _marshal(Safe);
    }
}
