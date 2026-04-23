using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>
/// Backend for Remote mode — REST for initial load + mutations, WebSocket for stream frames.
/// Mirrors RemoteBackend.swift.
/// </summary>
public sealed class RemoteBackend : IBackend
{
    public NodeStore Nodes { get; }
    public ServerStore Servers { get; }
    public ClientSettings ClientSettings { get; }
    public ServerSettings ServerSettings { get; }

    private readonly RemoteClient _client;
    private CancellationTokenSource? _streamCts;
    private Task? _streamTask;

    public RemoteBackend(
        RemoteClient client,
        NodeStore nodes,
        ServerStore servers,
        ClientSettings clientSettings,
        ServerSettings serverSettings)
    {
        _client = client;
        Nodes = nodes;
        Servers = servers;
        ClientSettings = clientSettings;
        ServerSettings = serverSettings;
    }

    public async Task StartAsync(CancellationToken ct = default)
    {
        var remoteNodes = await _client.ListNodesAsync(ct).ConfigureAwait(false);
        Nodes.Nodes.Clear();
        foreach (var rn in remoteNodes)
            Nodes.Nodes.Add(ToNode(rn));

        var settings = await _client.GetSettingsAsync(ct).ConfigureAwait(false);
        ApplyServerSettings(settings);

        _streamCts = new CancellationTokenSource();
        _streamTask = Task.Run(() => StreamLoopAsync(_streamCts.Token));
    }

    public async Task StopAsync(CancellationToken ct = default)
    {
        _streamCts?.Cancel();
        if (_streamTask is not null) { try { await _streamTask.ConfigureAwait(false); } catch { } }
    }

    public async Task AddNodeAsync(Node node, CancellationToken ct = default)
    {
        var created = await _client.CreateNodeAsync(ToRemote(node), ct).ConfigureAwait(false);
        Nodes.Add(ToNode(created));
    }

    public async Task UpdateNodeAsync(Node node, CancellationToken ct = default)
    {
        var updated = await _client.UpdateNodeAsync(ToRemote(node), ct).ConfigureAwait(false);
        Nodes.Update(ToNode(updated));
    }

    public async Task RemoveNodeAsync(Guid id, CancellationToken ct = default)
    {
        await _client.DeleteNodeAsync(id, ct).ConfigureAwait(false);
        Nodes.Remove(id);
    }

    public async Task BulkAddNodesAsync(IEnumerable<Node> nodes, CancellationToken ct = default)
    {
        foreach (var n in nodes) await AddNodeAsync(n, ct).ConfigureAwait(false);
    }

    public Task SetNodeEnabledAsync(Guid id, bool enabled, CancellationToken ct = default)
        => UpdateFieldAsync(id, n => n with { Enabled = enabled }, ct);

    public Task SetNodeFavoriteAsync(Guid id, bool favorite, CancellationToken ct = default)
        => UpdateFieldAsync(id, n => n with { Favorite = favorite }, ct);

    public Task SetNodeSnoozeAsync(Guid id, DateTime? until, CancellationToken ct = default)
        => UpdateFieldAsync(id, n => n with { SnoozedUntil = until }, ct);

    private async Task UpdateFieldAsync(Guid id, Func<Node, Node> f, CancellationToken ct)
    {
        var n = Nodes.ById(id); if (n == null) return;
        await UpdateNodeAsync(f(n), ct).ConfigureAwait(false);
    }

    public async Task UpdateServerSettingsAsync(CancellationToken ct = default)
    {
        var wire = new RemoteServerSettings(
            Thresholds: new RemoteThresholds(
                ServerSettings.Thresholds.CpuWarn, ServerSettings.Thresholds.CpuCritical,
                ServerSettings.Thresholds.MemWarn, ServerSettings.Thresholds.MemCritical,
                ServerSettings.Thresholds.DiskWarn, ServerSettings.Thresholds.DiskCritical),
            LocalPollingIntervalSeconds: ServerSettings.LocalPollingIntervalSeconds,
            SshPollingIntervalSeconds: ServerSettings.SshPollingIntervalSeconds,
            NotificationsEnabled: ServerSettings.NotificationsEnabled,
            NotifyWarn: ServerSettings.NotifyWarn,
            NotifyCritical: ServerSettings.NotifyCritical,
            NotifyDebounceSeconds: ServerSettings.NotifyDebounceSeconds,
            AutoUpdateSamplersEnabled: ServerSettings.AutoUpdateSamplersEnabled,
            PostWakeGraceSeconds: ServerSettings.PostWakeGraceSeconds);
        var applied = await _client.PutSettingsAsync(wire, ct).ConfigureAwait(false);
        ApplyServerSettings(applied);
    }

    private async Task StreamLoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try
            {
                await foreach (var frame in _client.StreamAsync(ct).ConfigureAwait(false))
                {
                    if (frame.Type == "sample" && frame.NodeId is Guid nid && frame.Sample is Sample s)
                        Servers.Ingest(nid, s);
                }
            }
            catch (OperationCanceledException) { return; }
            catch { /* reconnect after short backoff */ }
            try { await Task.Delay(TimeSpan.FromSeconds(2), ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
    }

    private void ApplyServerSettings(RemoteServerSettings s)
    {
        ServerSettings.Thresholds = new MetricThresholds(
            s.Thresholds.CpuWarn, s.Thresholds.CpuCritical,
            s.Thresholds.MemWarn, s.Thresholds.MemCritical,
            s.Thresholds.DiskWarn, s.Thresholds.DiskCritical);
        ServerSettings.LocalPollingIntervalSeconds = s.LocalPollingIntervalSeconds;
        ServerSettings.SshPollingIntervalSeconds = s.SshPollingIntervalSeconds;
        ServerSettings.NotificationsEnabled = s.NotificationsEnabled;
        ServerSettings.NotifyWarn = s.NotifyWarn;
        ServerSettings.NotifyCritical = s.NotifyCritical;
        ServerSettings.NotifyDebounceSeconds = s.NotifyDebounceSeconds;
        ServerSettings.AutoUpdateSamplersEnabled = s.AutoUpdateSamplersEnabled;
        ServerSettings.PostWakeGraceSeconds = s.PostWakeGraceSeconds;
    }

    private static Node ToNode(RemoteNode rn) => new()
    {
        Id = rn.Id,
        DisplayName = rn.DisplayName,
        Kind = rn.Kind == "ssh" ? NodeKind.Ssh : NodeKind.Local,
        SshUser = rn.SshUser,
        SshHost = rn.SshHost,
        Tags = rn.Tags?.ToList() ?? new List<string>(),
        Enabled = rn.Enabled,
        Favorite = rn.Favorite,
    };

    private static RemoteNode ToRemote(Node n) => new(
        Id: n.Id,
        DisplayName: n.DisplayName,
        Kind: n.Kind == NodeKind.Ssh ? "ssh" : "local",
        SshUser: n.SshUser,
        SshHost: n.SshHost,
        Tags: n.Tags,
        Enabled: n.Enabled,
        Favorite: n.Favorite);

    public async ValueTask DisposeAsync()
    {
        await StopAsync().ConfigureAwait(false);
        await _client.DisposeAsync().ConfigureAwait(false);
    }
}
