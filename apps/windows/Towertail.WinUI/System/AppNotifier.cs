using Microsoft.Windows.AppNotifications;
using Microsoft.Windows.AppNotifications.Builder;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Thin wrapper over <see cref="AppNotificationBuilder"/>. Produces a native Windows 11
/// toast with a <c>Launch</c> argument of <c>host=&lt;id&gt;&amp;metric=&lt;m&gt;</c> so
/// <see cref="NotificationTapRouter"/> can open the right full-view window.
/// </summary>
public sealed class AppNotifier
{
    private readonly AppNotificationManager _mgr;
    private bool _registered;

    public AppNotifier()
    {
        _mgr = AppNotificationManager.Default;
    }

    public void Register()
    {
        if (_registered) return;
        _mgr.Register();
        _registered = true;
    }

    public void Notify(string title, string body, Guid hostId, string metric)
    {
        var n = new AppNotificationBuilder()
            .AddText(title)
            .AddText(body)
            .SetTag(hostId.ToString())
            .AddArgument("host", hostId.ToString())
            .AddArgument("metric", metric)
            .BuildNotification();
        _mgr.Show(n);
    }
}
