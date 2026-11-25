using FluentAssertions;
using System.Text.Json;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
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

    [Fact]
    public async Task GetRulesAsyncDecodesRulesView()
    {
        var host = Guid.Parse("22222222-2222-2222-2222-222222222222");
        _server.Handlers["GET /v1/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            File.ReadAllText(Path.Combine("TestData", "rules-view.json")));

        var view = await _client.GetRulesAsync();

        _server.Requests.Single().Path.Should().Be("/v1/rules");
        view.Org.Thresholds.CpuWarn.Should().Be(0.8);
        view.Org.Muted.Set.Should().BeEquivalentTo([AlertSignal.Disk]);
        view.Org.Origin!.Source.Should().Be("web");
        view.Hosts.Should().HaveCount(2);
        var first = view.Hosts.Single(h => h.NodeId == host);
        first.Host.IgnoredProcs.Should().Equal("pg_dump");
        first.Effective.Thresholds.MemWarn.Should().Be(0.85);
        first.Effective.IsHostPair(ThresholdMetric.Mem).Should().BeTrue();
        first.Effective.Muted.Contains(AlertSignal.Long).Should().BeTrue();
    }

    [Fact]
    public async Task ImportRulesAsyncSendsPickedChanges()
    {
        var backup = Guid.NewGuid();
        _server.Handlers["POST /v1/rules/import"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            $$"""{"backupId":"{{backup}}","hosts":1,"org":true}""");
        var body = new RemoteRulesImport(
            new RemoteOrgRules { Muted = new MutedSignals([AlertSignal.Disk]) },
            [new RemoteRulesImportHost(Guid.Parse("22222222-2222-2222-2222-222222222222"), new RemoteHostRules { Muted = MutedSignals.None })],
            "windows", "test-pc");

        var result = await _client.ImportRulesAsync(body);

        var req = _server.Requests.Single();
        req.Method.Should().Be("POST");
        req.Authorization.Should().Be("Bearer test-token");
        var json = System.Text.Json.Nodes.JsonNode.Parse(req.Body)!;
        json["client"]!.GetValue<string>().Should().Be("windows");
        json["device"]!.GetValue<string>().Should().Be("test-pc");
        json["org"]!["muted"]!.AsArray().Single()!.GetValue<string>().Should().Be("disk");
        json["hosts"]!.AsArray().Single()!["nodeId"]!.GetValue<string>().Should().Be("22222222-2222-2222-2222-222222222222");
        result.BackupId.Should().Be(backup);
        result.Hosts.Should().Be(1);
        result.Org.Should().BeTrue();
    }

    [Fact]
    public async Task SetMuteAsyncSendsMetricAndNode()
    {
        var node = Guid.NewGuid();
        _server.Handlers["PUT /v1/mutes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200);

        await _client.SetMuteAsync(AlertSignal.Mem, node, true);

        var req = _server.Requests.Single();
        var json = System.Text.Json.Nodes.JsonNode.Parse(req.Body)!;
        json["metric"]!.GetValue<string>().Should().Be("mem");
        json["nodeId"]!.GetValue<string>().Should().Be(node.ToString());
        json["muted"]!.GetValue<bool>().Should().BeTrue();
    }

    [Fact]
    public async Task RulesCallForbiddenGivesPlainMessage()
    {
        _server.Handlers["PUT /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 403, """{"error":{"message":"admin only"}}""");

        var act = async () => await _client.PutHostRulesAsync(Guid.NewGuid(), new RemoteHostRules());

        var ex = (await act.Should().ThrowAsync<RemoteException>()).Which;
        ex.Status.Should().Be(403);
        ex.UserMessage.Should().NotBeNullOrEmpty();
    }

    private const string EmptySnapshot = """{"node_id":"00000000-0000-0000-0000-000000000001","ts":"2026-04-20T12:00:10Z","tier":"raw","bucket_s":0,"by":"cpu","processes":[]}""";

    [Fact]
    public async Task ProcessesAtHitsCorrectPathAndQuery()
    {
        var id = Guid.NewGuid();
        _server.Handlers["GET /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, EmptySnapshot);

        var snap = await _client.ProcessesAtAsync(id, new DateTime(2026, 4, 20, 12, 0, 10, DateTimeKind.Utc), ProcSort.Rss, 20);

        var req = _server.Requests.Single();
        req.Path.Should().Be($"/v1/nodes/{id}/processes/at");
        Uri.UnescapeDataString(req.Query).Should().Be("ts=2026-04-20T12:00:10Z&by=mem&limit=20");
        snap.Bytes.Should().Be(EmptySnapshot.Length);
    }

    [Fact]
    public async Task OffendersSendsSeries60()
    {
        var id = Guid.NewGuid();
        _server.Handlers["GET /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"node_id":"00000000-0000-0000-0000-000000000001","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","processes":[]}""");

        await _client.OffendersAsync(id, new DateTime(2026, 4, 20, 11, 0, 0, DateTimeKind.Utc),
            new DateTime(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc), ProcSort.Cpu, 10, 60);

        var req = _server.Requests.Single();
        req.Path.Should().Be($"/v1/nodes/{id}/processes");
        Uri.UnescapeDataString(req.Query).Should().Be("from=2026-04-20T11:00:00Z&to=2026-04-20T12:00:00Z&by=cpu&limit=10&series=60");
    }

    [Fact]
    public async Task ProcessKeyIsPercentEncoded()
    {
        var id = Guid.NewGuid();
        string? raw = null;
        _server.Handlers["GET /v1/nodes/"] = ctx =>
        {
            raw = ctx.Request.RawUrl;
            return LocalTestHttpServer.ReplyAsync(ctx, 200,
                """{"node_id":"00000000-0000-0000-0000-000000000001","key":"a/b+c","from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","points":[]}""");
        };

        var d = await _client.ProcessAsync(id, "a/b+c", new DateTime(2026, 4, 20, 11, 0, 0, DateTimeKind.Utc),
            new DateTime(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc), TimeSpan.FromSeconds(60));

        raw.Should().StartWith($"/v1/nodes/{id}/processes/a%2Fb%2Bc?");
        raw.Should().EndWith("&step=60s");
        d.Key.Should().Be("a/b+c");
    }

    [Fact]
    public async Task HistorySendsMetricsListAndStep()
    {
        var id = Guid.NewGuid();
        _server.Handlers["GET /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"node_id":"00000000-0000-0000-0000-000000000001","series":{"cpu":[{"ts":"2026-04-20T11:00:00Z","value":12.5,"max":40}],"mem":[]},"from":"2026-04-20T11:00:00Z","to":"2026-04-20T12:00:00Z","resolution":"60s"}""");

        var h = await _client.HistoryAsync(id, new[] { "cpu", "mem" },
            new DateTime(2026, 4, 20, 11, 0, 0, DateTimeKind.Utc), new DateTime(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc), TimeSpan.FromSeconds(60));

        var req = _server.Requests.Single();
        req.Path.Should().Be($"/v1/nodes/{id}/history");
        Uri.UnescapeDataString(req.Query).Should().Be("metrics=cpu,mem&from=2026-04-20T11:00:00Z&to=2026-04-20T12:00:00Z&step=60s");
        h.Series["cpu"].Should().ContainSingle().Which.Max.Should().Be(40);
        h.Series["mem"].Should().BeEmpty();
    }

    [Fact]
    public async Task BrokenJsonThrowsRemoteException()
    {
        _server.Handlers["GET /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, "{not json");

        var act = async () => await _client.DiskHistoryAsync(Guid.NewGuid(), DateTime.UtcNow.AddHours(-1), DateTime.UtcNow);

        await act.Should().ThrowAsync<RemoteException>();
    }
}
