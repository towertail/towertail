using System.Diagnostics;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.UIA3;
using Xunit;

namespace Towertail.UITests;

/// <summary>
/// Tier 3 smoke tests — FlaUI (UIA3) against the real app binary.
/// Runs only on an interactive Windows session with a freshly built
/// <c>Towertail.exe</c> on <c>PATH</c> (or via <c>TOWERTAIL_EXE</c>).
/// CI runs these nightly on a self-hosted runner; developer boxes run
/// them on demand via <c>scripts/test.ps1 --flaui</c>.
/// </summary>
[Trait("Category", "FlaUI")]
public sealed class TrayLaunchSmokeTests
{
    private static string ExePath =>
        Environment.GetEnvironmentVariable("TOWERTAIL_EXE") ?? "Towertail.exe";

    [Fact(Skip = "FlaUI — interactive session only. Run via scripts/test.ps1 --flaui")]
    public void AppStartsAndPopoverOpens()
    {
        // Baked-in Debug build flag forces the popover window to stay visible
        // during UIA inspection (WS_EX_NOACTIVATE + focus-loss would otherwise
        // hide it mid-probe). See app.manifest / App.xaml.cs --test-pin-popover.
        using var app = Application.Launch(new ProcessStartInfo
        {
            FileName = ExePath,
            Arguments = "--test-pin-popover",
            UseShellExecute = false,
        });
        using var automation = new UIA3Automation();

        // Tray icons don't surface as toplevel windows — we wait instead for the
        // pinned popover to appear after startup.
        var deadline = DateTime.UtcNow.AddSeconds(10);
        Window? popover = null;
        while (DateTime.UtcNow < deadline && popover is null)
        {
            foreach (var w in app.GetAllTopLevelWindows(automation))
            {
                if (w.Title?.Contains("Towertail", StringComparison.OrdinalIgnoreCase) == true)
                {
                    popover = w;
                    break;
                }
            }
            if (popover is null) Thread.Sleep(200);
        }

        Assert.NotNull(popover);
        Assert.True(popover!.IsAvailable);
    }
}
