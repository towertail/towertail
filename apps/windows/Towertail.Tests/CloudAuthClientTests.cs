using FluentAssertions;
using System.Text.Json;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Cloud;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudAuthClientTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private CloudAuthClient _client = null!;

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _server.AnonymousPrefixes.Add("/v1/auth/");
        _client = new CloudAuthClient(new Uri(_server.BaseUrl));
        return ValueTask.CompletedTask;
    }

    public ValueTask DisposeAsync() => _server.DisposeAsync();

    private void Reply(string key, int status, string json = "{}")
        => _server.Handlers[key] = ctx => LocalTestHttpServer.ReplyAsync(ctx, status, json);

    [Fact]
    public async Task VerifySendsWantTokenAndWindowsLabel()
    {
        Reply("POST /v1/auth/code/verify", 200, """{"token":"tok-1"}""");

        var token = await _client.VerifyAsync("a@b.co", "123456");

        token.Should().Be("tok-1");
        using var doc = JsonDocument.Parse(_server.Requests.Single().Body);
        doc.RootElement.GetProperty("email").GetString().Should().Be("a@b.co");
        doc.RootElement.GetProperty("code").GetString().Should().Be("123456");
        doc.RootElement.GetProperty("want").GetString().Should().Be("token");
        doc.RootElement.GetProperty("label").GetString().Should().Be("Towertail for Windows");
    }

    [Fact]
    public async Task BadEmailGivesBadEmail()
    {
        Reply("POST /v1/auth/code", 400);
        var act = () => _client.SendCodeAsync("nope");
        (await act.Should().ThrowAsync<CloudAuthException>()).Which.Kind.Should().Be(CloudAuthErrorKind.BadEmail);
    }

    [Fact]
    public async Task WrongCodeGivesWrongCode()
    {
        Reply("POST /v1/auth/code/verify", 401);
        var act = () => _client.VerifyAsync("a@b.co", "000000");
        (await act.Should().ThrowAsync<CloudAuthException>()).Which.Kind.Should().Be(CloudAuthErrorKind.WrongCode);
    }

    [Fact]
    public async Task LockedGivesLocked()
    {
        Reply("POST /v1/auth/code/verify", 429);
        var act = () => _client.VerifyAsync("a@b.co", "000000");
        (await act.Should().ThrowAsync<CloudAuthException>()).Which.Kind.Should().Be(CloudAuthErrorKind.Locked);
    }

    [Fact]
    public async Task MeDecodesRole()
    {
        Reply("GET /v1/me", 200, """{"user":{"email":"a@b.co"},"org":{"name":"Acme","plan":"free"},"role":"admin"}""");

        var me = await _client.MeAsync("test-token");

        me.User.Email.Should().Be("a@b.co");
        me.Org.Should().Be(new CloudOrg("Acme", "free"));
        me.Role.Should().Be("admin");
        _server.Requests.Single().Authorization.Should().Be("Bearer test-token");
    }

    [Fact]
    public async Task MeWithOldTokenGivesUnauthorized()
    {
        _server.ExpectedToken = "other";
        var act = () => _client.MeAsync("stale");
        (await act.Should().ThrowAsync<CloudAuthException>()).Which.Kind.Should().Be(CloudAuthErrorKind.Unauthorized);
    }

    [Fact]
    public async Task BillingNotFoundGivesNull()
    {
        Reply("GET /v1/billing", 404);
        (await _client.BillingAsync("test-token")).Should().BeNull();
    }

    [Fact]
    public async Task BillingDecodesRoomAndPayments()
    {
        Reply("GET /v1/billing", 200, """{"plan":{"id":"free","maxNodes":3},"nodes":2,"maxNodes":5,"payments":false}""");

        var billing = (await _client.BillingAsync("test-token"))!;

        billing.Plan.MaxNodes.Should().Be(3);
        billing.Room.Should().Be(3);
        billing.CanBuy.Should().BeFalse();
    }

    [Fact]
    public async Task HandoffGivesTheOneTimeLink()
    {
        Reply("POST /v1/auth/handoff", 200, """{"url":"https://app.example.com/?t=1"}""");
        _server.ExpectedToken = "test-token";

        var url = await _client.HandoffAsync("test-token", "/hosts");

        url.Should().Be(new Uri("https://app.example.com/?t=1"));
        _server.Requests.Single().Body.Should().Contain("\"redirect\":\"/hosts\"");
    }

    [Fact]
    public async Task DeletionNotFoundGivesNull()
    {
        Reply("GET /v1/me/deletion", 404);
        (await _client.DeletionAsync("test-token")).Should().BeNull();
    }

    [Fact]
    public async Task DeleteAccountSendsEmail()
    {
        Reply("DELETE /v1/me", 200);
        await _client.DeleteAccountAsync("test-token", "a@b.co");
        _server.Requests.Single().Body.Should().Contain("a@b.co");
    }

    [Fact]
    public async Task ServerErrorTextComesFromTheEnvelope()
    {
        Reply("GET /v1/me", 500, """{"error":{"message":"boom"}}""");
        var act = () => _client.MeAsync("test-token");
        var ex = (await act.Should().ThrowAsync<CloudAuthException>()).Which;
        ex.Kind.Should().Be(CloudAuthErrorKind.Server);
        ex.Message.Should().Be("boom");
    }

    [Fact]
    public async Task UnreachableServerGivesNetwork()
    {
        var client = new CloudAuthClient(new Uri("http://127.0.0.1:1"));
        var act = () => client.MeAsync("t");
        (await act.Should().ThrowAsync<CloudAuthException>()).Which.Kind.Should().Be(CloudAuthErrorKind.Network);
    }
}
