using FluentAssertions;
using System.Text.Json;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests;

public sealed class RemoteClientTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private RemoteClient _client = null!;

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _client.DisposeAsync();
        await _server.DisposeAsync();
    }

    [Fact]
    public async Task ListNodesReturnsSeededFleet()
    {
        var nodes = new[]
        {
            new RemoteNode(Guid.NewGuid(), "a", "ssh", "u", "h", null, true, false),
            new RemoteNode(Guid.NewGuid(), "b", "local", null, null, null, true, true),
        };
        _server.Handlers["GET /v1/nodes"] = async ctx =>
        {
            ctx.Response.ContentType = "application/json";
            var body = JsonSerializer.Serialize(nodes);
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync(body);
        };

        var result = await _client.ListNodesAsync();
        result.Should().HaveCount(2);
        result[0].DisplayName.Should().Be("a");
    }

    [Fact]
    public async Task SendsBearerTokenHeader()
    {
        _server.Handlers["GET /v1/nodes"] = async ctx =>
        {
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync("[]");
        };
        await _client.ListNodesAsync();
        _server.Requests.Single().Authorization.Should().Be("Bearer test-token");
    }

    [Fact]
    public async Task WrongTokenRejectedAs401()
    {
        _server.ExpectedToken = "other";
        _server.Handlers["GET /v1/nodes"] = async ctx =>
        {
            using var w = new StreamWriter(ctx.Response.OutputStream);
            await w.WriteAsync("[]");
        };
        var act = async () => await _client.ListNodesAsync();
        await act.Should().ThrowAsync<RemoteException>()
            .Where(e => e.Status == 401);
    }

    [Fact]
    public async Task DeleteNodeHitsCorrectPath()
    {
        _server.Handlers["DELETE /v1/nodes/"] = ctx => { ctx.Response.StatusCode = 204; ctx.Response.Close(); return Task.CompletedTask; };
        var id = Guid.NewGuid();
        await _client.DeleteNodeAsync(id);
        _server.Requests.Single().Path.Should().Be($"/v1/nodes/{id}");
    }
}
