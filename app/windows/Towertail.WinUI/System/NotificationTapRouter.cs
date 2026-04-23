using Microsoft.Windows.AppNotifications;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.FullView;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Resolves <c>NotificationActivated</c> args into an open <see cref="FullViewWindow"/> focused
/// on the host + metric tab the toast was about.
/// </summary>
public static class NotificationTapRouter
{
    public static void Handle(AppNotificationActivatedEventArgs args)
    {
        var map = args.Arguments;
        if (!map.TryGetValue("host", out var hostStr)) return;
        if (!Guid.TryParse(hostStr, out var hostId)) return;
        var metric = map.TryGetValue("metric", out var m) ? m : "cpu";

        var env = Towertail.WinUI.App.Current.Environment;
        var vm = env.Servers.Find(hostId);
        if (vm == null) return;

        TowertailApp.MainDispatcher?.TryEnqueue(() =>
        {
            var win = new FullViewWindow();
            win.Bind(vm);
            win.SelectTab(metric);
            win.Activate();
        });
    }
}
