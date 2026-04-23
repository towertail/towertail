using H.NotifyIcon;
using Microsoft.UI;
using Microsoft.UI.Xaml;
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
        };
        var iconSource = LoadIconSource();
        if (iconSource is not null) _icon.IconSource = iconSource;
        _icon.LeftClickCommand = new RelayCommand(TogglePopover);
        _icon.ForceCreate();
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
