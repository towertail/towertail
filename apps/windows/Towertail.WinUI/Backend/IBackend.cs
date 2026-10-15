using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>
/// Abstraction over the source of truth: local-file mode uses <see cref="LocalBackend"/>,
/// remote-server mode uses <see cref="RemoteBackend"/>. Surfaces the four observable stores
/// the UI binds to plus mutation entry points.
/// </summary>
public interface IBackend : IAsyncDisposable
{
    NodeStore Nodes { get; }
    ServerStore Servers { get; }
    ClientSettings ClientSettings { get; }
    ServerSettings ServerSettings { get; }

    Task AddNodeAsync(Node node, CancellationToken ct = default);
    Task UpdateNodeAsync(Node node, CancellationToken ct = default);
    Task RemoveNodeAsync(Guid id, CancellationToken ct = default);
    Task BulkAddNodesAsync(IEnumerable<Node> nodes, CancellationToken ct = default);

    Task SetNodeEnabledAsync(Guid id, bool enabled, CancellationToken ct = default);
    Task SetNodeFavoriteAsync(Guid id, bool favorite, CancellationToken ct = default);
    Task SetNodeSnoozeAsync(Guid id, DateTime? until, CancellationToken ct = default);

    Task UpdateServerSettingsAsync(CancellationToken ct = default);

    Task StartAsync(CancellationToken ct = default);
    Task StopAsync(CancellationToken ct = default);
}
