using FluentAssertions;
using System.Text.Json;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class RemoteBackendTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;

    public ValueTask InitializeAsync() { _server = new LocalTestHttpServer(); return ValueTask.CompletedTask; }
    public ValueTask DisposeAsync() => _server.DisposeAsync();

    [Fact]
    public async Task InitialLoadPopulatesNodesAndSettings()
    {
        var remoteNodes = new[]
        {
            new RemoteNode(Guid.NewGuid(), "db", "ssh", "u", "h", null, true, false)
        };
        _server.Handlers["GET /v1/nodes"] = async ctx =>
        {
            var body = JsonSerializer.Serialize(remoteNodes);
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync(body);
        };
        var settings = new RemoteServerSettings(
            new RemoteThresholds(0.7, 0.9, 0.7, 0.9, 0.8, 0.95),
            2, 10, false, true, true, 60, false, 15);
        _server.Handlers["GET /v1/settings"] = async ctx =>
        {
            var body = JsonSerializer.Serialize(settings);
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync(body);
        };

        var settingsPath = Path.GetTempFileName();
        var historyPath = Path.Combine(Path.GetTempPath(), $"tt-{Guid.NewGuid():N}.sqlite");
        try
        {
            await using var history = new HistoryStore(historyPath);
            var nodes = new NodeStore(settingsPath);
            var ss = new ServerSettings(settingsPath);
            var cs = new ClientSettings(settingsPath);
            var servers = new ServerStore(nodes, ss, history);
            var client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
            var backend = new RemoteBackend(client, nodes, servers, cs, ss);

            // Disable stream loop reconnect before asserting.
            await backend.StartAsync();
            await Task.Delay(100);

            nodes.Nodes.Should().HaveCount(1);
            nodes.Nodes[0].DisplayName.Should().Be("db");
            ss.Thresholds.CpuWarn.Should().BeApproximately(0.7, 0.0001);
            await backend.StopAsync();
            await backend.DisposeAsync();
        }
        finally
        {
            if (File.Exists(settingsPath)) File.Delete(settingsPath);
            if (File.Exists(historyPath)) File.Delete(historyPath);
        }
    }
}
