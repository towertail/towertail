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

    [Fact]
    public void EnrollRequestLeavesOutEmptyIds()
    {
        System.Text.Json.JsonSerializer.Serialize(new EnrollRequest("web")).Should().Be("""{"display_name":"web"}""");
        var id = Guid.Parse("00000000-0000-0000-0000-00000000000a");
        System.Text.Json.JsonSerializer.Serialize(new EnrollRequest("web", id, "m-1"))
            .Should().Be($$"""{"display_name":"web","node_id":"{{id}}","machine_id":"m-1"}""");
    }

    [Fact]
    public async Task AckAlertPostsToAckRoute()
    {
        var id = Guid.NewGuid();
        var node = Guid.NewGuid();
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            $$"""{"id":"{{id}}","node_id":"{{node}}","metric":"cpu","tint":"critical","acked_at":"2026-05-01T10:00:00Z"}""");

        var alert = await _client.AckAlertAsync(id);

        var req = _server.Requests.Single();
        req.Method.Should().Be("POST");
        req.Path.Should().Be($"/v1/alerts/{id}/ack");
        req.Authorization.Should().Be("Bearer test-token");
        alert.Id.Should().Be(id);
        alert.NodeId.Should().Be(node);
        alert.AckedAt.Should().NotBeNull();
        alert.IsSilenced(DateTimeOffset.UtcNow).Should().BeTrue();
    }

    [Fact]
    public async Task SnoozeAlertSendsUntil()
    {
        var id = Guid.NewGuid();
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            $$"""{"id":"{{id}}","node_id":"{{Guid.NewGuid()}}","metric":"cpu","tint":"warn","snoozed_until":"2026-05-01T11:00:00Z"}""");

        var alert = await _client.SnoozeAlertAsync(id, new DateTimeOffset(2026, 5, 1, 13, 0, 0, TimeSpan.FromHours(2)));

        var req = _server.Requests.Single();
        req.Path.Should().Be($"/v1/alerts/{id}/snooze");
        Uri.UnescapeDataString(req.Query).Should().Be("until=2026-05-01T11:00:00Z");
        alert.SnoozedUntil.Should().Be(new DateTimeOffset(2026, 5, 1, 11, 0, 0, TimeSpan.Zero));
    }

    [Fact]
    public async Task AckAlertThrowsOnForbidden()
    {
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 403);

        var act = async () => await _client.AckAlertAsync(Guid.NewGuid());

        await act.Should().ThrowAsync<RemoteException>().Where(e => e.Status == 403);
    }
}
