using H.NotifyIcon;
using H.NotifyIcon.Core;
using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media.Imaging;
using System.Runtime.InteropServices;
using Towertail.WinUI.Bootstrap;
using Windows.Foundation;

namespace Towertail.WinUI.MenuBar;

/// <summary>
/// Hosts the tray icon ("notification area" icon) for the app. Click → toggle popover.
/// Mirrors MenuBarIcon.swift on the Mac side.
/// </summary>
public sealed class TrayIconHost
{
    private readonly AppEnvironment _env;
    private TaskbarIcon? _icon;
    private TrayPopoverWindow? _popover;

    public TrayIconHost(AppEnvironment env) { _env = env; }

    public void Show()
    {
        _icon = new TaskbarIcon
        {
            ToolTipText = "Towertail",
            // H.NotifyIcon's MenuFlyout (WinUI XAML) needs a XamlRoot to pop
            // up; we create the icon from code, not XAML, so it has none.
            // Skip ContextFlyout and handle right-click explicitly with a
            // native Win32 popup menu — bulletproof and requires no host.
            ContextMenuMode = ContextMenuMode.SecondWindow,
        };
        var iconSource = LoadIconSource();
        if (iconSource is not null) _icon.IconSource = iconSource;
        _icon.LeftClickCommand = new RelayCommand(TogglePopover);
        _icon.RightClickCommand = new RelayCommand(ShowContextMenuNative);
        _icon.ForceCreate();
    }

    // Win32 popup menu constants
    private const uint MF_STRING = 0x00000000;
    private const uint MF_SEPARATOR = 0x00000800;
    private const uint TPM_RETURNCMD = 0x0100;
    private const uint TPM_RIGHTBUTTON = 0x0002;
    private const uint TPM_BOTTOMALIGN = 0x0020;
    private const uint WM_NULL = 0x0000;
    private const int WS_POPUP = unchecked((int)0x80000000);
    private const int HWND_MESSAGE = -3;

    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr CreatePopupMenu();
    [DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    private static extern bool AppendMenu(IntPtr hMenu, uint uFlags, uint uIDNewItem, string lpNewItem);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool DestroyMenu(IntPtr hMenu);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern int TrackPopupMenuEx(IntPtr hmenu, uint fuFlags, int x, int y, IntPtr hwnd, IntPtr lptpm);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool GetCursorPos(out POINT lpPoint);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool PostMessage(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam);
    [DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    private static extern IntPtr CreateWindowExW(int exStyle, string className, string? windowName,
        int style, int x, int y, int w, int h, IntPtr parent, IntPtr menu, IntPtr instance, IntPtr param);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern IntPtr GetModuleHandleW(string? lpModuleName);

    [StructLayout(LayoutKind.Sequential)]
    private struct POINT { public int X; public int Y; }

    private IntPtr _menuOwnerHwnd = IntPtr.Zero;

    private IntPtr EnsureMenuOwner()
    {
        if (_menuOwnerHwnd != IntPtr.Zero) return _menuOwnerHwnd;
        // Message-only window: hosts nothing visual, but TrackPopupMenuEx
        // will accept it as an owner and SetForegroundWindow can target it.
        // Using "STATIC" avoids having to register a class.
        _menuOwnerHwnd = CreateWindowExW(0, "STATIC", null, WS_POPUP,
            0, 0, 0, 0, new IntPtr(HWND_MESSAGE), IntPtr.Zero,
            GetModuleHandleW(null), IntPtr.Zero);
        return _menuOwnerHwnd;
    }

    private void ShowContextMenuNative()
    {
        var owner = EnsureMenuOwner();
        // Pull our hidden owner to the foreground so TrackPopupMenuEx does
        // not dismiss immediately when another app has focus.
        SetForegroundWindow(owner);

        var menu = CreatePopupMenu();
        try
        {
            AppendMenu(menu, MF_STRING, 1, "Open Towertail");
            AppendMenu(menu, MF_SEPARATOR, 0, string.Empty);
            AppendMenu(menu, MF_STRING, 2, "Exit");

            GetCursorPos(out var pt);
            var cmd = TrackPopupMenuEx(menu,
                TPM_RETURNCMD | TPM_RIGHTBUTTON | TPM_BOTTOMALIGN,
                pt.X, pt.Y, owner, IntPtr.Zero);

            // Per MSDN Q135788, post a null msg so the menu dismisses cleanly.
            PostMessage(owner, WM_NULL, IntPtr.Zero, IntPtr.Zero);

            switch (cmd)
            {
                case 1: TogglePopover(); break;
                case 2: Dispose(); Application.Current.Exit(); break;
            }
        }
        finally
        {
            DestroyMenu(menu);
        }
    }

    public void Dispose()
    {
        _icon?.Dispose();
        _icon = null;
    }

    private void TogglePopover()
    {
        _popover ??= new TrayPopoverWindow(_env);
        if (_popover.IsVisible) _popover.HidePopover();
        else _popover.ShowAtTray();
    }

    private static BitmapImage? LoadIconSource()
    {
        try
        {
            var path = Path.Combine(AppContext.BaseDirectory, "Assets", "AppIcon.ico");
            if (File.Exists(path))
                return new BitmapImage(new Uri(path));
        }
        catch { }
        return null;
    }

    private sealed class RelayCommand : System.Windows.Input.ICommand
    {
        private readonly Action _action;
        public RelayCommand(Action a) { _action = a; }
        public event EventHandler? CanExecuteChanged;
        public bool CanExecute(object? parameter) => true;
        public void Execute(object? parameter) => _action();
    }
}
