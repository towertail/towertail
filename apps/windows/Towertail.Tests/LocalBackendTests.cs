using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class LocalBackendTests : IAsyncLifetime
{
    private string _settings = "";
    private string _history = "";
    private HistoryStore _store = null!;
    private NodeStore _nodes = null!;
    private ServerStore _servers = null!;
    private ServerSettings _serverSettings = null!;
    private ClientSettings _clientSettings = null!;
    private LocalBackend _backend = null!;

    public ValueTask InitializeAsync()
    {
        _settings = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _history = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        _store = new HistoryStore(_history);
        _clientSettings = new ClientSettings(_settings);
        _serverSettings = new ServerSettings(_settings);
        _nodes = new NodeStore(_settings);
        _servers = new ServerStore(_nodes, _serverSettings, _store);
        _backend = new LocalBackend(_nodes, _servers, _clientSettings, _serverSettings);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _backend.DisposeAsync();
        await _store.DisposeAsync();
        if (File.Exists(_settings)) File.Delete(_settings);
        if (File.Exists(_history)) File.Delete(_history);
    }

    [Fact]
    public void WiresObservableStores()
    {
        _backend.Nodes.Should().BeSameAs(_nodes);
        _backend.Servers.Should().BeSameAs(_servers);
        _backend.ClientSettings.Should().BeSameAs(_clientSettings);
        _backend.ServerSettings.Should().BeSameAs(_serverSettings);
    }

    [Fact]
    public async Task NodeCRUDRoundTrips()
    {
        var n = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
        await _backend.AddNodeAsync(n);
        _backend.Nodes.ById(n.Id).Should().NotBeNull();

        await _backend.SetNodeEnabledAsync(n.Id, false);
        _backend.Nodes.ById(n.Id)!.Enabled.Should().BeFalse();

        await _backend.SetNodeFavoriteAsync(n.Id, true);
        _backend.Nodes.ById(n.Id)!.Favorite.Should().BeTrue();

        await _backend.RemoveNodeAsync(n.Id);
        _backend.Nodes.ById(n.Id).Should().BeNull();
    }

    [Fact]
    public async Task BulkAddNodes()
    {
        var ns = Enumerable.Range(0, 5).Select(i => new Node
        {
            DisplayName = $"n{i}", Kind = NodeKind.Ssh, SshUser = "u", SshHost = $"h{i}"
        }).ToList();
        await _backend.BulkAddNodesAsync(ns);
        _backend.Nodes.Nodes.Where(x => x.DisplayName.StartsWith("n")).Should().HaveCount(5);
    }

    [Fact]
    public async Task UpdateServerSettingsPersists()
    {
        _backend.ServerSettings.NotifyDebounceSeconds = 180;
        await _backend.UpdateServerSettingsAsync();
        var reloaded = new ServerSettings(_settings);
        reloaded.NotifyDebounceSeconds.Should().Be(180);
    }
}
