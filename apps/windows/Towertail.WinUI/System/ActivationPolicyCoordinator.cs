using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using WinRT.Interop;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Control whether secondary windows (Preferences, FullView) appear in the taskbar.
/// Mac equivalent: ActivationPolicyCoordinator toggling LSUIElement on the fly when the
/// user has real windows open.
/// </summary>
public static class ActivationPolicyCoordinator
{
    public static void ShowInTaskbar(Window window, bool show)
    {
        var hwnd = WindowNative.GetWindowHandle(window);
        var id = Win32Interop.GetWindowIdFromWindow(hwnd);
        var appWindow = AppWindow.GetFromWindowId(id);
        if (appWindow?.Presenter is OverlappedPresenter p)
        {
            p.IsAlwaysOnTop = false;
            p.SetBorderAndTitleBar(true, true);
        }
        appWindow?.Show(show);
    }
}
