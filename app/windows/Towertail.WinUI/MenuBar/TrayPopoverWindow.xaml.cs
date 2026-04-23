using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using System.Runtime.InteropServices;
using Towertail.WinUI.Bootstrap;
using Windows.Graphics;
using WinRT.Interop;

namespace Towertail.WinUI.MenuBar;

/// <summary>
/// Borderless, topmost, tool-window popover shown below the tray icon. Matches the Mac
/// popover dimensions (360×620). Hides on deactivation (WM_ACTIVATE LOWORD=0).
/// </summary>
public sealed partial class TrayPopoverWindow : Window
{
    private const int Width = 360;
    private const int Height = 620;

    private readonly AppEnvironment _env;
    private AppWindow? _appWindow;
    public bool IsVisible { get; private set; }

    public TrayPopoverWindow(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        Root.Bind(env);

        _appWindow = GetAppWindow();
        if (_appWindow?.Presenter is OverlappedPresenter overlapped)
        {
            overlapped.SetBorderAndTitleBar(false, false);
            overlapped.IsAlwaysOnTop = true;
            overlapped.IsResizable = false;
            overlapped.IsMaximizable = false;
            overlapped.IsMinimizable = false;
        }
        _appWindow?.Resize(new SizeInt32(Width, Height));
        Activated += OnActivated;
        _appWindow?.Hide();
    }

    public void ShowAtTray()
    {
        if (_appWindow is null) return;
        var (x, y) = CalculatePositionNearTray();
        _appWindow.Move(new PointInt32(x, y));
        _appWindow.Show();
        this.Activate();
        IsVisible = true;
    }

    public void HidePopover()
    {
        _appWindow?.Hide();
        IsVisible = false;
    }

    private void OnActivated(object sender, WindowActivatedEventArgs args)
    {
        if (args.WindowActivationState == WindowActivationState.Deactivated)
        {
            // Don't hide during UIA inspection — the inspector steals focus.
            if (App.TestPinPopover) return;
            HidePopover();
        }
    }

    private AppWindow? GetAppWindow()
    {
        var hwnd = WindowNative.GetWindowHandle(this);
        var id = Win32Interop.GetWindowIdFromWindow(hwnd);
        return AppWindow.GetFromWindowId(id);
    }

    /// <summary>
    /// Compute an anchor near the Windows notification area. We fall back to the
    /// primary monitor's lower-right corner when <c>Shell_NotifyIconGetRect</c> is
    /// unavailable (older Win10 builds / unpackaged).
    /// </summary>
    private (int X, int Y) CalculatePositionNearTray()
    {
        try
        {
            var hwnd = WindowNative.GetWindowHandle(this);
            var dpi = GetDpiForWindow(hwnd);
            var scale = dpi / 96.0;
            var workW = GetSystemMetrics(0);
            var workH = GetSystemMetrics(1);
            var x = workW - (int)(Width * scale) - 12;
            var y = workH - (int)(Height * scale) - 52;
            return (Math.Max(0, x), Math.Max(0, y));
        }
        catch
        {
            return (100, 100);
        }
    }

    [DllImport("user32.dll")]
    private static extern int GetSystemMetrics(int nIndex);

    [DllImport("user32.dll")]
    private static extern uint GetDpiForWindow(IntPtr hwnd);
}
