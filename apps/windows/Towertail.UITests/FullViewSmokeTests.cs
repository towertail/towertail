using System.Diagnostics;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Conditions;
using FlaUI.UIA3;
using Xunit;

namespace Towertail.UITests;

/// <summary>
/// Full-view smoke — opens the popover, clicks the first card, then
/// cycles through every metric tab in the detached detail window.
/// </summary>
[Trait("Category", "FlaUI")]
public sealed class FullViewSmokeTests
{
    private static string ExePath =>
        Environment.GetEnvironmentVariable("TOWERTAIL_EXE") ?? "Towertail.exe";

    [Fact(Skip = "FlaUI — interactive session only. Run via scripts/test.ps1 --flaui")]
    public void OpensFullViewAndSwitchesAllTabs()
    {
        using var app = Application.Launch(new ProcessStartInfo
        {
            FileName = ExePath,
            Arguments = "--test-pin-popover --mock-backend",
            UseShellExecute = false,
        });
        using var automation = new UIA3Automation();

        var popover = WaitForWindow(app, automation, "Towertail", TimeSpan.FromSeconds(10));
        Assert.NotNull(popover);

        // First card opens the FullViewWindow on double-click.
        var firstCard = popover!.FindFirstDescendant(cf => cf.ByAutomationId("ServerCard_0"));
        Assert.NotNull(firstCard);
        firstCard!.DoubleClick();

        var fullView = WaitForWindow(app, automation, "full view", TimeSpan.FromSeconds(10));
        Assert.NotNull(fullView);

        foreach (var tabName in new[] { "CPU", "MEM", "DISK", "NET", "PROCS" })
        {
            var tab = fullView!.FindFirstDescendant(cf => cf.ByName(tabName));
            Assert.NotNull(tab);
            tab!.Click();
            Thread.Sleep(100);
        }

        fullView!.Close();
    }

    private static Window? WaitForWindow(Application app, UIA3Automation automation, string titleContains, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            foreach (var w in app.GetAllTopLevelWindows(automation))
            {
                if (w.Title?.Contains(titleContains, StringComparison.OrdinalIgnoreCase) == true)
                    return w;
            }
            Thread.Sleep(200);
        }
        return null;
    }
}
