using Xunit;

namespace Towertail.UITests;

/// <summary>
/// Tier 3 smoke tests — FlaUI (UIA3) against the real app binary.
/// Runs only on an interactive Windows session with a freshly built Debug
/// <c>Towertail.exe</c> on <c>PATH</c> (or via <c>TOWERTAIL_EXE</c>).
/// Run them on demand via <c>scripts/test.ps1 --flaui</c>.
/// </summary>
[Trait("Category", "FlaUI")]
[Collection(AppUnderTest.Collection)]
public sealed class TrayLaunchSmokeTests
{
    [SkipWithoutFlaui]
    public void AppStartsAndPopoverOpens()
    {
        using var app = new AppUnderTest();

        var popover = app.WaitForPopover(TimeSpan.FromSeconds(10));

        Assert.NotNull(popover);
        Assert.True(popover!.IsAvailable);
    }
}
