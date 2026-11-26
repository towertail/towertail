using System.Diagnostics;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.UIA3;

namespace Towertail.UITests;

/// <summary>
/// A running <c>Towertail.exe</c> with its popover open. The app allows one
/// instance, so the tests of this assembly run one at a time (see <see cref="Collection"/>).
/// </summary>
internal sealed class AppUnderTest : IDisposable
{
    public const string Collection = "FlaUI";

    private static string ExePath =>
        Environment.GetEnvironmentVariable("TOWERTAIL_EXE") ?? "Towertail.exe";

    private readonly Application _app;
    private readonly UIA3Automation _automation = new();

    public AppUnderTest()
    {
        // The popover starts hidden. The Debug build opens and pins it for
        // TOWERTAIL_DEBUG_OPEN=popover, so the focus change of an inspector
        // does not hide it.
        var info = new ProcessStartInfo
        {
            FileName = ExePath,
            Arguments = "--test-pin-popover",
            UseShellExecute = false,
        };
        info.Environment["TOWERTAIL_DEBUG_OPEN"] = "popover";
        _app = Application.Launch(info);
    }

    public Window? WaitForWindow(Func<string, bool> titleMatches, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            foreach (var w in _app.GetAllTopLevelWindows(_automation))
            {
                if (titleMatches(w.Title ?? "")) return w;
            }
            Thread.Sleep(200);
        }
        return null;
    }

    public Window? WaitForPopover(TimeSpan timeout) =>
        WaitForWindow(title => title == "Towertail", timeout);

    public void Dispose()
    {
        _automation.Dispose();
        _app.Kill();
        _app.Dispose();
    }
}
