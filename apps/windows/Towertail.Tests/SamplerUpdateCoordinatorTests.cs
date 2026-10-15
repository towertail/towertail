using FluentAssertions;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class SamplerUpdateCoordinatorTests
{
    private sealed class FakePusher : ISamplerPusher
    {
        public int Calls;
        public bool Fail;
        public Task<string> PushAsync(Node node, CancellationToken ct = default)
        {
            Calls++;
            return Fail
                ? Task.FromException<string>(new InvalidOperationException("boom"))
                : Task.FromResult("linux-amd64");
        }
    }

    private static readonly SamplerManifest Manifest =
        new("1.0.0", "a5a3c8f", new Dictionary<string, string>());

    private static readonly Node Ssh =
        new() { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };

    private DateTime _now = new(2026, 1, 1, 0, 0, 0, DateTimeKind.Utc);

    private SamplerUpdateCoordinator Make(FakePusher pusher) => new(Manifest, pusher, now: () => _now);

    [Fact]
    public void ExpectedSamplerFieldIsVersionPlusSha()
        => Manifest.ExpectedSamplerField.Should().Be("1.0.0+a5a3c8f");

    [Fact]
    public async Task SameFieldDoesNotPush()
    {
        var pusher = new FakePusher();
        await Make(pusher).MaybeUpdateAsync(Ssh, "1.0.0+a5a3c8f", enabled: true);
        pusher.Calls.Should().Be(0);
    }

    [Fact]
    public async Task DifferentFieldPushesOnce()
    {
        var pusher = new FakePusher();
        var c = Make(pusher);
        await c.MaybeUpdateAsync(Ssh, "0.1.0+abc1234", enabled: true);
        await c.MaybeUpdateAsync(Ssh, "0.1.0+abc1234", enabled: true);
        pusher.Calls.Should().Be(1);
        c.LastError(Ssh.Id).Should().BeNull();
    }

    [Fact]
    public async Task SkipsWhenDisabledOrNotSsh()
    {
        var pusher = new FakePusher();
        var c = Make(pusher);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: false);
        await c.MaybeUpdateAsync(new Node { Kind = NodeKind.Local }, "old", enabled: true);
        await c.MaybeUpdateAsync(Ssh, "", enabled: true);
        pusher.Calls.Should().Be(0);
    }

    [Fact]
    public async Task SuccessCooldownHoldsFor15Seconds()
    {
        var pusher = new FakePusher();
        var c = Make(pusher);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        _now += TimeSpan.FromSeconds(14);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        pusher.Calls.Should().Be(1);
        _now += TimeSpan.FromSeconds(2);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        pusher.Calls.Should().Be(2);
    }

    [Fact]
    public async Task FailureCooldownHoldsFor300Seconds()
    {
        var pusher = new FakePusher { Fail = true };
        var c = Make(pusher);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        c.LastError(Ssh.Id).Should().Be("boom");
        _now += TimeSpan.FromSeconds(299);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        pusher.Calls.Should().Be(1);
        _now += TimeSpan.FromSeconds(2);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        pusher.Calls.Should().Be(2);
    }

    [Fact]
    public async Task CurrentFieldClearsLastError()
    {
        var pusher = new FakePusher { Fail = true };
        var c = Make(pusher);
        await c.MaybeUpdateAsync(Ssh, "old", enabled: true);
        await c.MaybeUpdateAsync(Ssh, "1.0.0+a5a3c8f", enabled: true);
        c.LastError(Ssh.Id).Should().BeNull();
    }
}
