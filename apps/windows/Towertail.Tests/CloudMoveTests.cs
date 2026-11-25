using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudMoveTests : IDisposable
{
    private readonly string _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
    private readonly string _prefsPath = Path.Combine(Path.GetTempPath(), $"tt-p-{Guid.NewGuid():N}.json");
    private readonly NodeStore _nodes;
    private readonly CloudPrefs _prefs;
    private CloudBilling? _billing;
    private bool _signedIn = true;
    private readonly CloudMove _move;

    public CloudMoveTests()
    {
        _nodes = new NodeStore(_settingsPath);
        _prefs = new CloudPrefs(_prefsPath);
        var lease = new ViewLeaseController(_ => Task.CompletedTask, new ManualDelay().Func);
        var installer = new CloudInstaller(
            () => null, _ => throw new InvalidOperationException("no SSH"), _nodes, lease, (_, _) => null,
            billing: () => Task.FromResult(_billing), planName: () => "free");
        _move = new CloudMove(_nodes, installer, _prefs, () => _signedIn, () => _billing);
    }

    public void Dispose()
    {
        foreach (var p in new[] { _settingsPath, _prefsPath })
            if (File.Exists(p)) File.Delete(p);
    }

    private Node Ssh(string name, CloudLink? link = null)
    {
        var node = new Node { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = name, Cloud = link };
        _nodes.Add(node);
        return node;
    }

    [Fact]
    public async Task OpenListsSshHostsWithNoLink()
    {
        var a = Ssh("a");
        Ssh("linked", new CloudLink(DateTime.UtcNow));
        _nodes.Add(Node.CloudOnly(Guid.NewGuid(), "db"));
        _nodes.Add(new Node { DisplayName = "local", Kind = NodeKind.Local });

        await _move.OpenAsync();

        _move.IsPresented.Should().BeTrue();
        _move.Phase.Should().Be(MovePhase.Pick);
        _move.Hosts.Select(h => h.Id).Should().Equal(a.Id);
        _move.Picks.Ids.Should().BeEquivalentTo([a.Id]);
    }

    [Fact]
    public async Task OpenPicksOnlyTheHostsThatFit()
    {
        var a = Ssh("a");
        Ssh("b");
        Ssh("c");
        _billing = new CloudBilling(new CloudPlan("free", 1), 0, 1);

        await _move.OpenAsync();

        _move.Picks.Ids.Should().BeEquivalentTo([a.Id]);
        _move.Picks.IsFull.Should().BeTrue();
        _move.NotPicked.Should().Be(2);
    }

    [Fact]
    public async Task TogglePicksAndUnpicks()
    {
        var a = Ssh("a");
        var b = Ssh("b");
        _billing = new CloudBilling(new CloudPlan("free", 1), 0, 1);
        await _move.OpenAsync();

        _move.Toggle(b.Id);
        _move.Picks.Ids.Should().BeEquivalentTo([a.Id]);
        _move.Toggle(a.Id);
        _move.Toggle(b.Id);

        _move.Picks.Ids.Should().BeEquivalentTo([b.Id]);
    }

    [Fact]
    public async Task ClearEmptiesThePicks()
    {
        Ssh("a");
        await _move.OpenAsync();
        _move.Clear();
        _move.Picks.Ids.Should().BeEmpty();
    }

    [Fact]
    public async Task StartMovesToTheInstallPhase()
    {
        var a = Ssh("a");
        await _move.OpenAsync();

        await _move.StartAsync();

        _move.Phase.Should().Be(MovePhase.Install);
        _move.Installer.ResultOf(a.Id).Should().NotBeNull();
    }

    [Fact]
    public async Task StartWithNoPicksDoesNothing()
    {
        Ssh("a");
        await _move.OpenAsync();
        _move.Clear();

        await _move.StartAsync();

        _move.Phase.Should().Be(MovePhase.Pick);
    }

    [Fact]
    public async Task OfferOnceOpensOneTime()
    {
        Ssh("a");

        await _move.OfferOnceAsync();
        _move.IsPresented.Should().BeTrue();
        _prefs.MoveOffered.Should().BeTrue();

        _move.Close();
        await _move.OfferOnceAsync();
        _move.IsPresented.Should().BeFalse();
    }

    [Fact]
    public async Task OfferOnceSkipsWithNoSshHosts()
    {
        await _move.OfferOnceAsync();
        _move.IsPresented.Should().BeFalse();
        _prefs.MoveOffered.Should().BeFalse();
    }

    [Fact]
    public async Task OfferOnceSkipsWhenSignedOut()
    {
        Ssh("a");
        _signedIn = false;
        await _move.OfferOnceAsync();
        _move.IsPresented.Should().BeFalse();
    }

    [Fact]
    public async Task OfferOnceSkipsWhenThePlanIsFull()
    {
        Ssh("a");
        _billing = new CloudBilling(new CloudPlan("free", 1), 1, 1);
        await _move.OfferOnceAsync();
        _move.IsPresented.Should().BeFalse();
        _move.IsFull.Should().BeTrue();
    }

    [Fact]
    public async Task ChangedFiresOnOpenToggleAndClose()
    {
        var a = Ssh("a");
        var count = 0;
        _move.Changed += () => count++;

        await _move.OpenAsync();
        _move.Toggle(a.Id);
        _move.Close();

        count.Should().Be(3);
    }

    [Fact]
    public void CanMoveNeedsSignInAndHosts()
    {
        _move.CanMove.Should().BeFalse();
        Ssh("a");
        _move.CanMove.Should().BeTrue();
        _signedIn = false;
        _move.CanMove.Should().BeFalse();
    }
}

public sealed class CloudPicksTests
{
    private static Node N(string name) => new() { DisplayName = name, Kind = NodeKind.Ssh };

    [Fact]
    public void CounterNamesTheRoom()
    {
        var picks = new CloudPicks([N("a"), N("b")], 3);
        picks.Counter.Should().Be("2 of 3 Cloud hosts selected");
        new CloudPicks([N("a")], 1).Counter.Should().Be("1 of 1 Cloud host selected");
        new CloudPicks([N("a")]).Counter.Should().Be("1 Cloud host selected");
    }

    [Fact]
    public void NoteTellsTheRoom()
    {
        CloudPicks.Note(null, "Free plan", false).Should().EndWith("SSH hosts have no limit.");
        CloudPicks.Note(0, "Free plan", true).Should().Be("The Free plan is full. Turn off 24/7 monitoring on another host first.");
        CloudPicks.Note(2, "Free plan", true).Should().Contain("has room for 2 more Cloud hosts");
        CloudPicks.Note(1, "Plan", false).Should().Contain("Your plan takes 1 Cloud host.");
    }
}
