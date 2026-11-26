using FlaUI.Core.Conditions;
using Xunit;

namespace Towertail.UITests;

/// <summary>
/// Full-view smoke — opens the popover, clicks the first card, then
/// cycles through every tab of the full view window.
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

        // A tap on the name of the first card bubbles to the card.
        var firstCardName = popover!.FindFirstDescendant(cf => cf.ByAutomationId("NameText"));
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
