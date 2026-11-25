using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class WorkbenchCursorTests
{
    private static readonly DateTime Now = new(2026, 11, 25, 12, 0, 0, DateTimeKind.Utc);

    [Fact]
    public void DragOf120SecondsOrMoreZooms()
    {
        var c = new WorkbenchCursor();
        c.BeginSelection(Now.AddMinutes(-30));
        c.UpdateSelection(Now.AddMinutes(-20));
        c.EndSelection();
        c.Zoom.Should().Be((Now.AddMinutes(-30), Now.AddMinutes(-20)));
        c.Selection.Should().BeNull();
        c.Window(Now).Should().Be(c.Zoom!.Value);
    }

    [Fact]
    public void DragRightToLeftZoomsInOrder()
    {
        var c = new WorkbenchCursor();
        c.BeginSelection(Now.AddMinutes(-10));
        c.UpdateSelection(Now.AddMinutes(-20));
        c.EndSelection();
        c.Zoom.Should().Be((Now.AddMinutes(-20), Now.AddMinutes(-10)));
    }

    [Fact]
    public void ShortDragDoesNotZoom()
    {
        var c = new WorkbenchCursor();
        c.BeginSelection(Now.AddMinutes(-30));
        c.UpdateSelection(Now.AddMinutes(-30).AddSeconds(119));
        c.EndSelection();
        c.Zoom.Should().BeNull();
        c.Selection.Should().BeNull();
    }

    [Fact]
    public void EscapeClosesPanelThenUnpins()
    {
        var c = new WorkbenchCursor { PinnedAt = Now.AddMinutes(-5), OpenProcess = new ProcessRef("k", 1, "node", "node x") };
        c.HandleEscape().Should().BeTrue();
        c.OpenProcess.Should().BeNull();
        c.PinnedAt.Should().NotBeNull();
        c.HandleEscape().Should().BeTrue();
        c.PinnedAt.Should().BeNull();
        c.HandleEscape().Should().BeFalse();
    }

    [Fact]
    public void StepPinClampsToWindowAndNow()
    {
        var c = new WorkbenchCursor { PinnedAt = Now.AddSeconds(-5) };
        c.StepPin(1, TimeSpan.FromMinutes(1), Now).Should().BeTrue();
        c.PinnedAt.Should().Be(Now);
        c.PinnedAt = Now.AddMinutes(-59);
        c.StepPin(-1, TimeSpan.FromMinutes(5), Now);
        c.PinnedAt.Should().Be(Now.AddHours(-1));
    }

    [Fact]
    public void StepPinIgnoredWhenLive()
    {
        var c = new WorkbenchCursor();
        c.StepPin(1, TimeSpan.FromMinutes(1), Now).Should().BeFalse();
        c.PinnedAt.Should().BeNull();
    }

    [Fact]
    public void RangeChangeClearsZoomAndPin()
    {
        var c = new WorkbenchCursor { PinnedAt = Now, HoverAt = Now, Zoom = (Now.AddMinutes(-9), Now.AddMinutes(-3)) };
        c.Range = HostRange.H6;
        c.Zoom.Should().BeNull();
        c.PinnedAt.Should().BeNull();
        c.HoverAt.Should().BeNull();
        c.Window(Now).From.Should().Be(Now.AddHours(-6));
    }

    [Fact]
    public void SpacePinsNewestThenReturnsToLive()
    {
        var c = new WorkbenchCursor();
        c.PinLatest(Now);
        c.PinnedAt.Should().Be(Now);
        c.PinLatest(Now);
        c.PinnedAt.Should().BeNull();
    }

    [Fact]
    public void EffectiveFollowsHoverThenPinThenLatest()
    {
        var c = new WorkbenchCursor();
        c.Effective(Now).Should().Be(Now);
        c.PinnedAt = Now.AddMinutes(-2);
        c.Effective(Now).Should().Be(Now.AddMinutes(-2));
        c.HoverAt = Now.AddMinutes(-3);
        c.Effective(Now).Should().Be(Now.AddMinutes(-3));
        c.State.Should().Be(CursorState.Hover);
    }

    [Fact]
    public void RangeAvailabilityFollowsOldestData()
    {
        WorkbenchCursor.IsAvailable(HostRange.H1, null, Now).Should().BeTrue();
        WorkbenchCursor.IsAvailable(HostRange.H6, null, Now).Should().BeFalse();
        WorkbenchCursor.IsAvailable(HostRange.H6, Now.AddHours(-1), Now).Should().BeTrue();
        WorkbenchCursor.IsAvailable(HostRange.H24, Now.AddHours(-3), Now).Should().BeFalse();
    }

    [Fact]
    public void ZoomUsesFinerStep()
    {
        var c = new WorkbenchCursor { Range = HostRange.D7 };
        c.HistoryStep(Now).Should().Be(TimeSpan.FromSeconds(1_800));
        c.Zoom = (Now.AddMinutes(-60), Now);
        c.HistoryStep(Now).Should().Be(TimeSpan.FromSeconds(12));
    }
}
