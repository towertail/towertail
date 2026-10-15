using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>
/// Local-only backend — reads/writes settings.json on this machine, no network.
/// </summary>
public sealed class LocalBackend : IBackend
{
    public NodeStore Nodes { get; }
    public ServerStore Servers { get; }
    public ClientSettings ClientSettings { get; }
    public ServerSettings ServerSettings { get; }

    public LocalBackend(NodeStore nodes, ServerStore servers, ClientSettings cs, ServerSettings ss)
    {
        Nodes = nodes;
        Servers = servers;
        ClientSettings = cs;
        ServerSettings = ss;
    }

    public Task AddNodeAsync(Node node, CancellationToken ct = default)
    {
        Nodes.Add(node);
        return Task.CompletedTask;
    }

    public Task UpdateNodeAsync(Node node, CancellationToken ct = default)
    {
        Nodes.Update(node);
        return Task.CompletedTask;
    }

    public Task RemoveNodeAsync(Guid id, CancellationToken ct = default)
    {
        Nodes.Remove(id);
        return Task.CompletedTask;
    }

    public Task BulkAddNodesAsync(IEnumerable<Node> nodes, CancellationToken ct = default)
    {
        Nodes.AddMany(nodes);
        return Task.CompletedTask;
    }

    public Task SetNodeEnabledAsync(Guid id, bool enabled, CancellationToken ct = default)
    {
        Nodes.SetEnabled(id, enabled);
        return Task.CompletedTask;
    }

    public Task SetNodeFavoriteAsync(Guid id, bool favorite, CancellationToken ct = default)
    {
        Nodes.SetFavorite(id, favorite);
        return Task.CompletedTask;
    }

    public Task SetNodeSnoozeAsync(Guid id, DateTime? until, CancellationToken ct = default)
    {
        Nodes.SetSnooze(id, until);
        return Task.CompletedTask;
    }

    public Task UpdateServerSettingsAsync(CancellationToken ct = default)
    {
        ServerSettings.Persist();
        return Task.CompletedTask;
    }

    public Task StartAsync(CancellationToken ct = default) => Task.CompletedTask;
    public Task StopAsync(CancellationToken ct = default) => Task.CompletedTask;

    public ValueTask DisposeAsync() => ValueTask.CompletedTask;
}
