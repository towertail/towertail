using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudSessionTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private FakeTokenVault _vault = null!;
    private CloudPrefs _prefs = null!;
    private HistoryStore _history = null!;
    private NodeStore _nodes = null!;
    private CloudSession _session = null!;
    private readonly FailurePoster _poster = new();
    private static readonly DateTimeOffset Now = new(2026, 5, 1, 10, 0, 0, TimeSpan.Zero);
    private string _prefsPath = "";
    private string _settingsPath = "";
    private string _historyPath = "";

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _server.AnonymousPrefixes.Add("/v1/auth/");
        _server.ExpectedToken = "tok-1";
        _server.Handlers["POST /v1/auth/code/verify"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, """{"token":"tok-1"}""");
        _server.Handlers["POST /v1/auth/code"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200);
        _server.Handlers["GET /v1/me"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            """{"user":{"email":"a@b.co"},"org":{"name":"Acme","plan":"free"},"role":"owner"}""");
        _server.Handlers["GET /v1/billing"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 404);
        _server.Handlers["GET /v1/stream"] = _server.AcceptWebSocketAsync;
        _server.Handlers["GET /v1/nodes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, "[]");
        _server.Handlers["GET /v1/alerts"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, """{"alerts":[]}""");

        _vault = new FakeTokenVault();
        _prefsPath = Path.Combine(Path.GetTempPath(), $"tt-cloud-{Guid.NewGuid():N}.json");
        _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _historyPath = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        var endpoint = new Uri(_server.BaseUrl);
        _prefs = new CloudPrefs(_prefsPath) { Endpoint = endpoint };
        _history = new HistoryStore(_historyPath);
        _nodes = new NodeStore(_settingsPath);
        var servers = new ServerStore(_nodes, new ServerSettings(_settingsPath), _history);
        var account = new CloudAccount(endpoint, _vault, new CloudAuthClient(endpoint), _prefs);
        _session = new CloudSession(account, _nodes, servers, _prefs, null, null, _poster, () => Now);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _session.StopFeedAsync();
        await _server.DisposeAsync();
        await _history.DisposeAsync();
        foreach (var p in new[] { _prefsPath, _settingsPath, _historyPath })
            if (File.Exists(p)) File.Delete(p);
    }

    private string TokenKey => $"127.0.0.1:{_server.Port}";

    private static async Task WaitUntil(Func<bool> condition)
    {
        var until = DateTime.UtcNow.AddSeconds(5);
        while (!condition())
        {
            if (DateTime.UtcNow > until) throw new TimeoutException();
            await Task.Delay(10);
        }
    }

    [Fact]
    public async Task NoTokenKeepsFeedNull()
    {
        await _session.StartAsync();

        _session.Feed.Should().BeNull();
        _session.Client.Should().BeNull();
    }

    [Fact]
    public async Task RestoreWithTokenStartsFeed()
    {
        _vault.Tokens[TokenKey] = "tok-1";

        await _session.StartAsync();

        _session.Feed.Should().NotBeNull();
        _session.Client.Should().NotBeNull();
        await WaitUntil(() => _session.Feed!.IsConnected);
    }

    [Fact]
    public async Task SignInStartsFeed()
    {
        await _session.Account.SendCodeAsync("a@b.co");
        await _session.Account.VerifyAsync("123456");

        _session.Feed.Should().NotBeNull();
        await WaitUntil(() => _session.Feed!.IsConnected);
    }

    [Fact]
    public async Task StartFeedTwiceKeepsTheFeed()
    {
        _vault.Tokens[TokenKey] = "tok-1";
        await _session.StartAsync();
        var feed = _session.Feed;

        _session.StartFeed();

        _session.Feed.Should().BeSameAs(feed);
    }

    [Fact]
    public async Task SignOutReleasesHostsAndRemovesCloudOnly()
    {
        var cloudOnly = Node.CloudOnly(Guid.NewGuid(), "db");
        var linked = new Node { DisplayName = "web", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", Cloud = new CloudLink(DateTime.UtcNow) };
        var plain = new Node { DisplayName = "other", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h2" };
        _nodes.Add(cloudOnly);
        _nodes.Add(linked);
        _nodes.Add(plain);
        _vault.Tokens[TokenKey] = "tok-1";
        await _session.StartAsync();

        await _session.SignOutAsync();

        _session.Feed.Should().BeNull();
        _session.Client.Should().BeNull();
        _session.Account.IsSignedIn.Should().BeFalse();
        _vault.Tokens.Should().NotContainKey(TokenKey);
        _nodes.ById(cloudOnly.Id).Should().BeNull();
        _nodes.ById(linked.Id)!.Cloud.Should().BeNull();
        _nodes.ById(plain.Id).Should().NotBeNull();
        _session.Released.Ids.Should().BeEquivalentTo([linked.Id]);
    }

    [Fact]
    public async Task TagForFollowsSignInAndFeed()
    {
        var ssh = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
        _session.TagFor(ssh).Should().BeNull();

        _vault.Tokens[TokenKey] = "tok-1";
        await _session.StartAsync();

        _session.TagFor(ssh).Should().Be(HostTag.SshOnly);
        await WaitUntil(() => _session.Feed!.IsConnected);
        _session.TagFor(Node.CloudOnly(Guid.NewGuid(), "web")).Should().Be(HostTag.Cloud);
    }

    [Fact]
    public async Task StateChangedFiresOnSignInAndSignOut()
    {
        var count = 0;
        _session.StateChanged += () => Interlocked.Increment(ref count);

        await _session.Account.SendCodeAsync("a@b.co");
        await _session.Account.VerifyAsync("123456");
        await WaitUntil(() => _session.Feed!.IsConnected);
        var afterSignIn = count;
        await _session.SignOutAsync();

        afterSignIn.Should().BeGreaterThan(0);
        count.Should().BeGreaterThan(afterSignIn);
    }

    private sealed class FailurePoster : ICloudAlertPoster
    {
        public List<string> Failures { get; } = [];
        public void Post(Towertail.WinUI.Backend.StreamEvent e, string hostName) { }
        public void Withdraw(Guid alertId) { }
        public void PostFailure(string text) => Failures.Add(text);
    }

    private async Task SignInAsync()
    {
        _vault.Tokens[TokenKey] = "tok-1";
        await _session.StartAsync();
    }

    [Fact]
    public async Task PerformAckCallsTheAckRoute()
    {
        var id = Guid.NewGuid();
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            $$"""{"id":"{{id}}","node_id":"{{Guid.NewGuid()}}","metric":"cpu","tint":"warn"}""");
        await SignInAsync();

        await _session.PerformAsync(CloudAlertAction.Ack, id);

        _server.Requests.Should().Contain(r => r.Method == "POST" && r.Path == $"/v1/alerts/{id}/ack");
        _poster.Failures.Should().BeEmpty();
    }

    [Fact]
    public async Task PerformSnoozeSendsOneHourFromNow()
    {
        var id = Guid.NewGuid();
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200,
            $$"""{"id":"{{id}}","node_id":"{{Guid.NewGuid()}}","metric":"cpu","tint":"warn"}""");
        await SignInAsync();

        await _session.PerformAsync(CloudAlertAction.Snooze, id);

        var req = _server.Requests.Single(r => r.Path.EndsWith("/snooze"));
        Uri.UnescapeDataString(req.Query).Should().Be("until=2026-05-01T11:00:00Z");
    }

    [Fact]
    public async Task PerformFailureShowsAToast()
    {
        _server.Handlers["POST /v1/alerts/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 403);
        await SignInAsync();

        await _session.PerformAsync(CloudAlertAction.Ack, Guid.NewGuid());

        _poster.Failures.Should().Equal("Ack failed: server said 403");
    }

    [Fact]
    public async Task PerformWithoutSignInDoesNothing()
    {
        await _session.PerformAsync(CloudAlertAction.Ack, Guid.NewGuid());

        _server.Requests.Should().BeEmpty();
        _poster.Failures.Should().BeEmpty();
    }

    [Theory]
    [InlineData("ack", CloudAlertAction.Ack)]
    [InlineData("snooze", CloudAlertAction.Snooze)]
    public void ToastActionIdsMapToActions(string id, CloudAlertAction action)
    {
        CloudAlertActions.Parse(id).Should().Be(action);
        action.Id().Should().Be(id);
    }

    [Fact]
    public void TapHasNoAction()
    {
        CloudAlertActions.Parse(null).Should().BeNull();
        CloudAlertActions.Parse("open").Should().BeNull();
    }
}
