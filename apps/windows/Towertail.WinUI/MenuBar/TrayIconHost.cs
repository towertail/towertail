using H.NotifyIcon;
using H.NotifyIcon.Core;
using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Dispatching;
using Microsoft.Win32;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;
using Windows.Foundation;
using Windows.UI.ViewManagement;
using DrawingIcon = System.Drawing.Icon;

namespace Towertail.WinUI.MenuBar;

/// <summary>
/// Hosts the tray icon ("notification area" icon) for the app. Click → toggle popover.
/// Mirrors MenuBarIcon.swift on the Mac side: the icon follows
/// <see cref="ServerStore.AggregateLevel"/> and <see cref="ClientSettings.TrayIconStyle"/>.
/// </summary>
public sealed class TrayIconHost
{
    private readonly AppEnvironment _env;
    private TaskbarIcon? _icon;
    private TrayPopoverWindow? _popover;
    private DispatcherQueue? _queue;
    // Keep a reference: ColorValuesChanged stops when UISettings is collected.
    private readonly UISettings _uiSettings = new();
    private readonly Dictionary<string, DrawingIcon> _icons = new();

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
        _queue = DispatcherQueue.GetForCurrentThread();
        UpdateIcon();
        _env.Servers.PropertyChanged += OnStateChanged;
        _env.ClientSettings.PropertyChanged += OnStateChanged;
        _uiSettings.ColorValuesChanged += (_, _) => _queue?.TryEnqueue(UpdateIcon);
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
        _env.Servers.PropertyChanged -= OnStateChanged;
        _env.ClientSettings.PropertyChanged -= OnStateChanged;
        _icon?.Dispose();
        _icon = null;
    }

    private void TogglePopover()
    {
        _popover ??= new TrayPopoverWindow(_env);
        if (_popover.IsVisible) _popover.HidePopover();
        else _popover.ShowAtTray();
    }

    private void OnStateChanged(object? sender, PropertyChangedEventArgs e)
    {
        if (e.PropertyName is nameof(ServerStore.AggregateLevel) or nameof(ClientSettings.TrayIconStyle))
            _queue?.TryEnqueue(UpdateIcon);
    }

    private void UpdateIcon()
    {
        if (_icon is null) return;
        var level = _env.Servers.AggregateLevel;
        var name = $"tray-{(TaskbarIsLight() ? "light" : "dark")}-{level.ToString().ToLowerInvariant()}";
        if (level != HealthLevel.Nominal && _env.ClientSettings.TrayIconStyle == TrayIconStyle.Full) name += "-full";
        if (LoadIcon(name) is { } icon) _icon.Icon = icon;
    }

    private DrawingIcon? LoadIcon(string name)
    {
        if (_icons.TryGetValue(name, out var cached)) return cached;
        try
        {
            var path = Path.Combine(AppContext.BaseDirectory, "Assets", "Tray", name + ".ico");
            if (!File.Exists(path)) return null;
            // Pick the frame for the small-icon size at the current DPI.
            var size = GetSystemMetrics(SM_CXSMICON);
            return _icons[name] = new DrawingIcon(path, size, size);
        }
        catch { return null; }
    }

    /// <summary>The taskbar has its own theme, separate from the app theme.</summary>
    private static bool TaskbarIsLight()
    {
        using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize");
        return key?.GetValue("SystemUsesLightTheme") is int v && v == 1;
    }

    private const int SM_CXSMICON = 49;
    [DllImport("user32.dll")]
    private static extern int GetSystemMetrics(int nIndex);

    private sealed class RelayCommand : System.Windows.Input.ICommand
    {
        private readonly Action _action;
        public RelayCommand(Action a) { _action = a; }
        public event EventHandler? CanExecuteChanged;
        public bool CanExecute(object? parameter) => true;
        public void Execute(object? parameter) => _action();
    }
}
