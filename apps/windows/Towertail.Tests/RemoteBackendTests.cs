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

    [Fact]
    public async Task LegacyStreamIngestsSampleFrames()
    {
        var node = new RemoteNode(Guid.NewGuid(), "db", "ssh", "u", "h", null, true, false);
        _server.Handlers["GET /v1/nodes"] = async ctx =>
        {
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync(JsonSerializer.Serialize(new[] { node }));
        };
        var settings = new RemoteServerSettings(
            new RemoteThresholds(0.7, 0.9, 0.7, 0.9, 0.8, 0.95), 2, 10, false, true, true, 60, false, 15);
        _server.Handlers["GET /v1/settings"] = async ctx =>
        {
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync(JsonSerializer.Serialize(settings));
        };
        _server.Handlers["GET /v1/stream"] = _server.AcceptWebSocketAsync;

        var settingsPath = Path.GetTempFileName();
        var historyPath = Path.Combine(Path.GetTempPath(), $"tt-{Guid.NewGuid():N}.sqlite");
        try
        {
            await using var history = new HistoryStore(historyPath);
            var nodes = new NodeStore(settingsPath);
            var ss = new ServerSettings(settingsPath);
            var servers = new ServerStore(nodes, ss, history);
            var client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
            var backend = new RemoteBackend(client, nodes, servers, new ClientSettings(settingsPath), ss);
            await backend.StartAsync();

            var frame = $$$$"""{"type":"sample","nodeId":"{{{{node.Id}}}}","sample":{"v":1,"ts":"2026-05-01T10:00:00Z","host":{"name":"db","os":"linux","arch":"amd64","kernel":"6","uptime_s":1,"sampler":"s"},"cpu":{"pct":55,"load_1":0,"load_5":0,"load_15":0,"cores":2},"mem":{"used":1,"total":2},"swap":{"used":0,"total":0},"errors":[]}}""";
            // StartAsync fills the node list without events, so rebuild the view model.
            var local = nodes.ById(node.Id)!;
            nodes.Remove(node.Id);
            nodes.Add(local);
            var vm = servers.Find(node.Id)!;
            var until = DateTime.UtcNow.AddSeconds(5);
            while (vm.CpuPct is null && DateTime.UtcNow < until)
            {
                await _server.SendToAllWebSocketsAsync(frame);
                await Task.Delay(50);
            }

            vm.CpuPct.Should().BeApproximately(55, 0.0001);
            _server.Requests.Should().Contain(r => r.Path == "/v1/stream" && r.Query == "");
            await backend.DisposeAsync();
        }
        finally
        {
            if (File.Exists(settingsPath)) File.Delete(settingsPath);
            if (File.Exists(historyPath)) File.Delete(historyPath);
        }
    }
}
