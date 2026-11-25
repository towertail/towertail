using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Cloud;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudAccountTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private FakeTokenVault _vault = null!;
    private CloudPrefs _prefs = null!;
    private string _prefsPath = "";
    private DateTime _now = new(2026, 5, 1, 10, 0, 0, DateTimeKind.Utc);
    private ManualDelay _delay = null!;
    private CloudAccount _account = null!;
    private readonly List<CloudStatus> _statuses = new();

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _server.AnonymousPrefixes.Add("/v1/auth/");
        _server.ExpectedToken = "tok-1";
        _vault = new FakeTokenVault();
        _prefsPath = Path.Combine(Path.GetTempPath(), $"tt-cloud-{Guid.NewGuid():N}.json");
        _prefs = new CloudPrefs(_prefsPath);
        _delay = new ManualDelay();
        var endpoint = new Uri(_server.BaseUrl);
        _account = new CloudAccount(endpoint, _vault, new CloudAuthClient(endpoint), _prefs,
            now: () => _now, delay: _delay.Func);
        _account.StatusChanged += s => _statuses.Add(s);
        Reply("POST /v1/auth/code/verify", 200, """{"token":"tok-1"}""");
        Reply("POST /v1/auth/code", 200);
        Reply("GET /v1/me", 200, Me("owner"));
        Reply("GET /v1/billing", 404);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _server.DisposeAsync();
        if (File.Exists(_prefsPath)) File.Delete(_prefsPath);
    }

    private static string Me(string role)
        => $$"""{"user":{"email":"a@b.co"},"org":{"name":"Acme","plan":"free"},"role":"{{role}}"}""";

    private void Reply(string key, int status, string json = "{}")
        => _server.Handlers[key] = ctx => LocalTestHttpServer.ReplyAsync(ctx, status, json);

    private string TokenKey => $"127.0.0.1:{_server.Port}";

    [Fact]
    public async Task VerifySavesTokenAndSignsIn()
    {
        await _account.SendCodeAsync(" a@b.co ");
        _account.Status.Should().Be(new CloudStatus.CodeSent("a@b.co", _now));
        _account.PendingEmail.Should().Be("a@b.co");
        _prefs.Email.Should().Be("a@b.co");

        await _account.VerifyAsync("123456");

        _vault.Tokens.Should().ContainKey(TokenKey).WhoseValue.Should().Be("tok-1");
        _account.Token.Should().Be("tok-1");
        _account.Status.Should().BeOfType<CloudStatus.SignedIn>();
        _account.Me!.Org.Name.Should().Be("Acme");
        _account.PendingEmail.Should().BeNull();
        _statuses.Select(s => s.GetType()).Should().Equal(typeof(CloudStatus.CodeSent), typeof(CloudStatus.SignedIn));
    }

    [Fact]
    public async Task WrongCodeKeepsPendingEmail()
    {
        Reply("POST /v1/auth/code/verify", 401);
        await _account.SendCodeAsync("a@b.co");

        await _account.VerifyAsync("000000");

        _account.Status.Should().Be(new CloudStatus.Failed("The code is not correct."));
        _account.PendingEmail.Should().Be("a@b.co");
        _vault.Tokens.Should().BeEmpty();
    }

    [Fact]
    public async Task ResendWaits60Seconds()
    {
        await _account.SendCodeAsync("a@b.co");
        _account.ResendWait.Should().Be(TimeSpan.FromSeconds(60));

        _now += TimeSpan.FromSeconds(20);
        _account.ResendWait.Should().Be(TimeSpan.FromSeconds(40));
        await _account.SendCodeAsync("a@b.co");
        _server.Requests.Count(r => r.Path == "/v1/auth/code").Should().Be(1);

        _now += TimeSpan.FromSeconds(41);
        _account.ResendWait.Should().Be(TimeSpan.Zero);
        await _account.SendCodeAsync("a@b.co");
        _server.Requests.Count(r => r.Path == "/v1/auth/code").Should().Be(2);
    }

    [Fact]
    public async Task OtherEmailSendsAtOnce()
    {
        await _account.SendCodeAsync("a@b.co");
        await _account.SendCodeAsync("c@d.co");
        _server.Requests.Count(r => r.Path == "/v1/auth/code").Should().Be(2);
    }

    [Fact]
    public async Task RestoreWithTokenSignsIn()
    {
        _vault.Set(TokenKey, "tok-1");
        await _account.RestoreAsync();
        _account.Status.Should().BeOfType<CloudStatus.SignedIn>();
    }

    [Fact]
    public async Task RestoreWithoutTokenStaysSignedOut()
    {
        await _account.RestoreAsync();
        _account.Status.Should().BeOfType<CloudStatus.SignedOut>();
        _statuses.Should().BeEmpty();
    }

    [Fact]
    public async Task RestoreRetriesAfter30Seconds()
    {
        _vault.Set(TokenKey, "tok-1");
        Reply("GET /v1/me", 500);

        await _account.RestoreAsync();

        _account.Status.Should().BeOfType<CloudStatus.Failed>();
        _account.Token.Should().Be("tok-1");
        _delay.Pending.Should().Equal(TimeSpan.FromSeconds(30));

        Reply("GET /v1/me", 200, Me("member"));
        _delay.Release(TimeSpan.FromSeconds(30));
        await WaitUntil(() => _account.Status is CloudStatus.SignedIn);
    }

    [Fact]
    public async Task UnauthorizedRestoreDeletesToken()
    {
        _vault.Set(TokenKey, "stale");
        _server.ExpectedToken = "other";
        Reply("GET /v1/me", 401);

        await _account.RestoreAsync();

        _vault.Tokens.Should().BeEmpty();
        _account.Token.Should().BeNull();
        _account.Status.Should().BeOfType<CloudStatus.SignedOut>();
        _delay.Pending.Should().BeEmpty();
    }

    [Fact]
    public async Task SignOutDeletesToken()
    {
        _vault.Set(TokenKey, "tok-1");
        await _account.RestoreAsync();

        await _account.SignOutAsync();

        _vault.Tokens.Should().BeEmpty();
        _account.Token.Should().BeNull();
        _account.Billing.Should().BeNull();
        _account.Status.Should().BeOfType<CloudStatus.SignedOut>();
    }

    [Fact]
    public async Task HandleUnauthorizedSignsOutOnce()
    {
        _vault.Set(TokenKey, "tok-1");
        await _account.RestoreAsync();
        _statuses.Clear();

        _account.HandleUnauthorized();
        _account.HandleUnauthorized();

        _statuses.Should().ContainSingle().Which.Should().BeOfType<CloudStatus.SignedOut>();
        _vault.Tokens.Should().BeEmpty();
    }

    [Fact]
    public void TokenKeyIsHostAndPort()
    {
        var local = new CloudAccount(new Uri("http://127.0.0.1:8080"), _vault, new CloudAuthClient(new Uri("http://127.0.0.1:8080")), _prefs);
        local.TokenKey.Should().Be("127.0.0.1:8080");
        var cloud = new CloudAccount(new Uri("https://api.towertail.com"), _vault, new CloudAuthClient(new Uri("https://api.towertail.com")), _prefs);
        cloud.TokenKey.Should().Be("api.towertail.com");
        cloud.DashboardUri.Should().Be(new Uri("https://app.towertail.com"));
        local.DashboardUri.Should().Be(new Uri("http://127.0.0.1:8080"));
    }

    [Theory]
    [InlineData("owner", true, true)]
    [InlineData("admin", true, true)]
    [InlineData("member", false, true)]
    [InlineData("viewer", false, false)]
    public void IsAdminAndCanMuteFollowRole(string role, bool admin, bool mute)
    {
        var me = new CloudMe(new CloudUser("a@b.co"), new CloudOrg("Acme", "free"), role);
        me.IsAdmin.Should().Be(admin);
        me.CanMute.Should().Be(mute);
    }

    [Fact]
    public async Task DashboardLinkUsesHandoffAndFallsBack()
    {
        _vault.Set(TokenKey, "tok-1");
        await _account.RestoreAsync();
        Reply("POST /v1/auth/handoff", 200, """{"url":"https://app.example.com/?t=1"}""");

        (await _account.OpenDashboardUriAsync()).Should().Be(new Uri("https://app.example.com/?t=1"));

        Reply("POST /v1/auth/handoff", 500);
        (await _account.OpenDashboardUriAsync()).Should().Be(_account.DashboardUri);
    }

    [Fact]
    public async Task DeleteAccountSignsOut()
    {
        _vault.Set(TokenKey, "tok-1");
        await _account.RestoreAsync();
        Reply("DELETE /v1/me", 200);

        await _account.DeleteAccountAsync("a@b.co");

        _account.Status.Should().BeOfType<CloudStatus.SignedOut>();
        _vault.Tokens.Should().BeEmpty();
    }

    private static async Task WaitUntil(Func<bool> condition)
    {
        var until = DateTime.UtcNow.AddSeconds(5);
        while (!condition())
        {
            if (DateTime.UtcNow > until) throw new TimeoutException();
            await Task.Delay(10);
        }
    }
}
