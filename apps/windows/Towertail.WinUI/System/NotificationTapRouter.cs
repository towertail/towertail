using Microsoft.Windows.AppNotifications;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.FullView;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Resolves <c>NotificationActivated</c> args into an open <see cref="FullViewWindow"/> focused
/// on the host + metric tab the toast was about. A button of a cloud alert toast goes to
/// <see cref="ActionHandler"/>.
/// </summary>
public static class NotificationTapRouter
{
    /// <summary>Runs the button of a cloud toast: the action and the alert id. Set at start-up.</summary>
    public static Func<CloudAlertAction, Guid, Task>? ActionHandler { get; set; }

    public static void Handle(AppNotificationActivatedEventArgs args)
    {
        var map = args.Arguments;
        if (map.TryGetValue("action", out var actionId) && CloudAlertActions.Parse(actionId) is { } action)
        {
            if (map.TryGetValue("alert", out var alertStr) && Guid.TryParse(alertStr, out var alertId)
                && ActionHandler is { } run)
                _ = Task.Run(() => run(action, alertId));
            return;
        }
        if (!map.TryGetValue("host", out var hostStr)) return;
        if (!Guid.TryParse(hostStr, out var hostId)) return;
        var metric = map.TryGetValue("metric", out var m) ? m : "cpu";

        var env = Towertail.WinUI.App.Current.Environment;
        var vm = env.Servers.Find(hostId);
        if (vm == null) return;

        TowertailApp.MainDispatcher?.TryEnqueue(() =>
        {
            var win = FullViewRegistry.Open(vm);
            win.SelectTab(metric);
        });
    }
}
