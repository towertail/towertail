using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests;

public sealed class ViewLeaseControllerTests
{
    private static readonly Guid A = Guid.Parse("00000000-0000-0000-0000-00000000000a");
    private static readonly Guid B = Guid.Parse("00000000-0000-0000-0000-00000000000b");
    private static readonly Guid C = Guid.Parse("00000000-0000-0000-0000-00000000000c");

    private readonly List<LiveView> _sent = new();
    private readonly ManualDelay _delay = new();
    private readonly ViewLeaseController _lease;

    public ViewLeaseControllerTests()
    {
        _lease = new ViewLeaseController(v => { _sent.Add(v); return Task.CompletedTask; }, _delay.Func);
    }

    [Fact]
    public void PopoverShowSendsVisibleCards()
    {
        _lease.SetPopover(true, [B, A]);
        _sent.Should().ContainSingle().Which.Should().Be(new LiveView([A, B]));
    }

    [Fact]
    public void HideSendsEmptyViewAtOnce()
    {
        _lease.SetPopover(true, [A]);
        _lease.SetPopover(false, []);
        _sent.Should().HaveCount(2);
        _sent[1].Should().Be(LiveView.Empty);
        _delay.Pending.Should().NotContain(ViewLeaseController.RenewEvery);
    }

    [Fact]
    public void HideWithoutShowSendsNothing()
    {
        _lease.SetPopover(false, []);
        _sent.Should().BeEmpty();
    }

    [Fact]
    public void RenewsEvery30Seconds()
    {
        _lease.SetPopover(true, [A]);
        _delay.Release(ViewLeaseController.RenewEvery);
        _delay.Release(ViewLeaseController.RenewEvery);
        var expected = new LiveView([A]);
        _sent.Should().HaveCount(3).And.OnlyContain(v => v.Equals(expected));
    }

    [Fact]
    public void LimitStopsRenewUntilChange()
    {
        _lease.SetPopover(true, [A]);
        _lease.DidReceiveLimit();
        _delay.Release(ViewLeaseController.RenewEvery).Should().BeFalse();
        _sent.Should().ContainSingle();

        _lease.SetWatched(B, true);
        _sent.Should().HaveCount(2);
        _delay.Release(ViewLeaseController.RenewEvery).Should().BeTrue();
        _sent.Should().HaveCount(3);
    }

    [Fact]
    public void ReconnectResendsView()
    {
        _lease.SetPopover(true, [A]);
        _lease.StreamDidConnect();
        _sent.Should().HaveCount(2);
        _sent[1].Should().Be(_sent[0]);
    }

    [Fact]
    public void ReconnectWithEmptyViewSendsNothing()
    {
        _lease.StreamDidConnect();
        _sent.Should().BeEmpty();
    }

    [Fact]
    public void CardChangesDebounce250ms()
    {
        _lease.SetPopover(true, [A]);
        _sent.Clear();

        _lease.SetCard(B, true);
        _lease.SetCard(C, true);
        _sent.Should().BeEmpty();
        _delay.Pending.Should().Contain(ViewLeaseController.CardDebounce);

        _delay.Release(ViewLeaseController.CardDebounce);
        _sent.Should().ContainSingle().Which.Should().Be(new LiveView([A, B, C]));
    }

    [Fact]
    public void FullViewAddsDetail()
    {
        var window = new object();
        _lease.SetFullView(window, A, DetailTab.Disks, true);
        _sent.Should().ContainSingle().Which.Should().Be(new LiveView([A], new ViewDetail(A, DetailTab.Disks)));

        _lease.SetFullView(window, A, DetailTab.Ports, true);
        _sent[^1].Detail.Should().Be(new ViewDetail(A, DetailTab.Ports));

        _lease.SetFullView(window, A, null, false);
        _sent[^1].Should().Be(LiveView.Empty);
    }

    [Fact]
    public void FullViewWithoutTabLeasesTheHostOnly()
    {
        _lease.SetFullView(new object(), A, null, true);
        _sent.Should().ContainSingle().Which.Should().Be(new LiveView([A]));
    }

    [Fact]
    public void SleepSendsEmptyViewAndWakeSendsTheViewAgain()
    {
        _lease.SetPopover(true, [A]);
        _lease.SetSystemAwake(false);
        _sent[^1].Should().Be(LiveView.Empty);
        _lease.SetSystemAwake(true);
        _sent[^1].Should().Be(new LiveView([A]));
    }

    [Fact]
    public void CapsAt500Hosts()
    {
        var hosts = Enumerable.Range(0, 600).Select(_ => Guid.NewGuid()).ToList();
        _lease.SetPopover(true, hosts);
        _sent.Should().ContainSingle().Which.Hosts.Should().HaveCount(500);
    }

    [Fact]
    public void StopCancelsTheTimers()
    {
        _lease.SetPopover(true, [A]);
        _lease.Stop();
        _delay.Pending.Should().BeEmpty();
    }

    [Fact]
    public void ListedHostsJoinTheView()
    {
        _lease.SetListed([B, A]);
        _sent.Should().ContainSingle().Which.Should().Be(new LiveView([A, B]));
        _delay.Pending.Should().Contain(ViewLeaseController.RenewEvery);
    }

    [Fact]
    public void ListedHostsAddToCards()
    {
        _lease.SetPopover(true, [A]);
        _lease.SetListed([C]);
        _sent[^1].Should().Be(new LiveView([A, C]));
    }

    [Fact]
    public void AnEmptyListEndsTheLease()
    {
        _lease.SetListed([A]);
        _lease.SetListed([]);
        _sent[^1].Should().Be(LiveView.Empty);
    }

    [Fact]
    public void TheSameListSendsNothingNew()
    {
        _lease.SetListed([A]);
        _lease.SetListed([A]);
        _sent.Should().HaveCount(1);
    }
}
