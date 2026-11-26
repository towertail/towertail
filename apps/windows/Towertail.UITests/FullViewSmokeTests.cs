using FlaUI.Core.Conditions;
using Xunit;

namespace Towertail.UITests;

/// <summary>
/// Tier 3 smoke test — FlaUI (UIA3) against the real app binary. It opens the popover,
/// clicks the first card, then cycles through every tab of the full view window.
/// Runs only on an interactive Windows session with a freshly built Debug
/// <c>Towertail.exe</c> on <c>PATH</c> (or via <c>TOWERTAIL_EXE</c>).
/// Run it on demand via <c>scripts/test.ps1 --flaui</c>.
/// </summary>
[Trait("Category", "FlaUI")]
[Collection(AppUnderTest.Collection)]
public sealed class FullViewSmokeTests
{
    [SkipWithoutFlaui]
    public void OpensFullViewAndSwitchesAllTabs()
    {
        using var app = new AppUnderTest();

        var popover = app.WaitForPopover(TimeSpan.FromSeconds(10));
        Assert.NotNull(popover);
        Assert.True(popover!.IsAvailable);

        // A tap on the name of the first card bubbles to the card.
        var firstCardName = popover.FindFirstDescendant(cf => cf.ByAutomationId("NameText"));
        Assert.NotNull(firstCardName);
        firstCardName!.Click();

        var fullView = app.WaitForWindow(title => title.StartsWith("Towertail — ", StringComparison.Ordinal), TimeSpan.FromSeconds(10));
        Assert.NotNull(fullView);

        foreach (var tabName in new[] { "OVERVIEW", "PROCESSES", "HEALTH", "DISKS", "ALERTS", "SETTINGS" })
        {
            var tab = fullView!.FindFirstDescendant(cf => cf.ByName(tabName));
            Assert.NotNull(tab);
            tab!.Click();
            Thread.Sleep(100);
        }
    }
}
