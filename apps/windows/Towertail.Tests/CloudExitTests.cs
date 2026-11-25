using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudExitTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private FakeTokenVault _vault = null!;
    private NodeStore _nodes = null!;
    private HistoryStore _history = null!;
    private CloudSession _session = null!;
    private CloudExit _exit = null!;
    private readonly Dictionary<Guid, FakeSamplerSession> _sessions = [];
    private readonly string[] _paths =
    [
        Path.Combine(Path.GetTempPath(), $"tt-cloud-{Guid.NewGuid():N}.json"),
        Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json"),
        Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite"),
    ];

    private string TokenKey => $"127.0.0.1:{_server.Port}";

    public async ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer { ExpectedToken = "tok-1" };
        _server.Handlers["GET /v1/me/deletion"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"email":"a@b.co","orgs":[{"id":"00000000-0000-0000-0000-000000000001","name":"Acme","role":"owner","fate":"delete","members":1,"nodes":1}],"blocked":false}""");
        _server.Handlers["GET /v1/me"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"user":{"email":"a@b.co"},"org":{"name":"Acme","plan":"free"},"role":"owner"}""");
        _server.Handlers["DELETE /v1/me"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 204);
        _server.Handlers["DELETE /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 204);
        _server.Handlers["GET /v1/billing"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 404);
        // No stream: the feed never connects, so it never syncs the node list behind the test.
        // The feed drops the link of a host that the list lacks, so the list follows the store.
        _server.Handlers["GET /v1/nodes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, JsonSerializer.Serialize(
            _nodes.Nodes.ToList().Where(n => n.IsCloud).Select(n => new RemoteNode(n.Id, n.DisplayName, "ssh", "u", "h", null, true, false))));
        _server.Handlers["GET /v1/alerts"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, """{"alerts":[]}""");

        _vault = new FakeTokenVault();
        var endpoint = new Uri(_server.BaseUrl);
        var prefs = new CloudPrefs(_paths[0]) { Endpoint = endpoint };
        _history = new HistoryStore(_paths[2]);
        _nodes = new NodeStore(_paths[1]);
        var servers = new ServerStore(_nodes, new ServerSettings(_paths[1]), _history);
        var account = new CloudAccount(endpoint, _vault, new CloudAuthClient(endpoint), prefs);
        _session = new CloudSession(account, _nodes, servers, prefs, null, null);
        _vault.Tokens[TokenKey] = "tok-1";
        await _session.StartAsync();

        var installer = new CloudInstaller(
            () => _session.Client, node => _sessions[node.Id] = new FakeSamplerSession(), _nodes, _session.Lease,
            (_, _) => null);
        _exit = new CloudExit(_session, installer, _nodes);
    }

    public async ValueTask DisposeAsync()
    {
        await _session.StopFeedAsync();
        await _server.DisposeAsync();
        await _history.DisposeAsync();
        foreach (var p in _paths)
            if (File.Exists(p)) File.Delete(p);
    }

    private Node Linked(string name)
    {
        var node = new Node { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = name, Cloud = new CloudLink(DateTime.UtcNow) };
        _nodes.Add(node);
        return node;
    }

    private IEnumerable<string> Calls => _server.Requests.Where(r => r.Method == "DELETE").Select(r => r.Path);

    [Fact]
    public async Task LeaveMovesHostsBackThenSignsOut()
    {
        var a = Linked("a");

        await _exit.LeaveAsync();

        _exit.Phase.Should().Be(ExitPhase.Done);
        _session.Account.IsSignedIn.Should().BeFalse();
        Calls.Should().Contain($"/v1/nodes/{a.Id}");
        _sessions[a.Id].Commands.Should().Contain("~/.towertail/towertail-sampler boot disable");
    }

    [Fact]
    public async Task LeaveWithNoHostsJustSignsOut()
    {
        await _exit.LeaveAsync();

        _exit.Phase.Should().Be(ExitPhase.Done);
        _session.Account.IsSignedIn.Should().BeFalse();
        Calls.Should().BeEmpty();
    }

    [Fact]
    public async Task AFailedMoveKeepsTheSignIn()
    {
        var a = Linked("alpha");
        Linked("beta");
        _server.Handlers["DELETE /v1/nodes/"] = ctx =>
            LocalTestHttpServer.ReplyAsync(ctx, ctx.Request.Url!.AbsolutePath.EndsWith(a.Id.ToString()) ? 500 : 204, "boom");

        await _exit.LeaveAsync();

        _exit.Phase.Should().Be(ExitPhase.Failed);
        _exit.Error.Should().Be("These hosts did not move back: alpha.");
        _exit.Failures.Keys.Should().BeEquivalentTo([a.Id]);
        _session.Account.IsSignedIn.Should().BeTrue();
    }

    [Fact]
    public async Task SignOutAnywaySignsOutAfterAFailure()
    {
        Linked("alpha");
        _server.Handlers["DELETE /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 500, "boom");
        await _exit.LeaveAsync();

        await _exit.SignOutAnywayAsync();

        _session.Account.IsSignedIn.Should().BeFalse();
        _exit.Phase.Should().Be(ExitPhase.Idle);
        _exit.Failures.Should().BeEmpty();
    }

    [Fact]
    public async Task DeleteMovesHostsFirstThenDeletesTheAccount()
    {
        var a = Linked("a");

        await _exit.DeleteAccountAsync("a@b.co");

        _exit.Phase.Should().Be(ExitPhase.Done);
        _session.Account.IsSignedIn.Should().BeFalse();
        Calls.Should().Equal($"/v1/nodes/{a.Id}", "/v1/me");
    }

    [Fact]
    public async Task ABlockedDeleteTouchesNothing()
    {
        Linked("a");
        _server.Handlers["GET /v1/me/deletion"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"email":"a@b.co","orgs":[{"id":"00000000-0000-0000-0000-000000000001","name":"Acme","role":"owner","fate":"blocked","members":3,"nodes":1,"reason":"owns a shared org"}],"blocked":true}""");

        await _exit.DeleteAccountAsync("a@b.co");

        _exit.Phase.Should().Be(ExitPhase.Failed);
        _exit.Error.Should().Contain("Acme: owns a shared org");
        Calls.Should().BeEmpty();
        _session.Account.IsSignedIn.Should().BeTrue();
    }

    [Fact]
    public async Task AServerThatCannotDeleteFails()
    {
        _server.Handlers["GET /v1/me/deletion"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 404);

        await _exit.DeleteAccountAsync("a@b.co");

        _exit.Phase.Should().Be(ExitPhase.Failed);
        _exit.Error.Should().Be("This Cloud server cannot delete accounts yet.");
    }

    [Fact]
    public async Task AFailedAccountDeleteKeepsTheSignIn()
    {
        _server.Handlers["DELETE /v1/me"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 500, "boom");

        await _exit.DeleteAccountAsync("a@b.co");

        _exit.Phase.Should().Be(ExitPhase.Failed);
        _session.Account.IsSignedIn.Should().BeTrue();
    }

    [Fact]
    public void ListsLinkedAndCloudOnlyHosts()
    {
        var a = Linked("a");
        var db = Node.CloudOnly(Guid.NewGuid(), "db");
        _nodes.Add(db);

        _exit.LinkedHosts.Select(h => h.Id).Should().Equal(a.Id);
        _exit.CloudOnlyHosts.Select(h => h.Id).Should().Equal(db.Id);
    }
}
