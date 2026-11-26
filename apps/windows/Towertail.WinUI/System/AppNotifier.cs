using Microsoft.Windows.AppNotifications;
using Microsoft.Windows.AppNotifications.Builder;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Thin wrapper over <see cref="AppNotificationBuilder"/>. Produces a native Windows 11
/// toast with the arguments <c>host</c> and <c>metric</c>, so <see cref="NotificationTapRouter"/>
/// can open the right full-view window. Posts the toasts of cloud alerts with the buttons
/// "Ack" and "Snooze 1 h".
/// </summary>
public sealed class AppNotifier(Logger? log = null) : INotificationSink, ICloudAlertPoster
{
    private readonly AppNotificationManager _mgr = AppNotificationManager.Default;
    private bool _registered;

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
        Show(n);
    }

    /// <summary>
    /// Shows the toast of a cloud alert event. A later event of the same alert replaces it. A
    /// change to ok has no buttons.
    /// </summary>
    public void Post(StreamEvent e, string hostName)
    {
        if (e.AlertId is not { } alertId) return;
        var alert = alertId.ToString();
        var builder = new AppNotificationBuilder()
            .AddText(e.Title ?? hostName)
            .AddText(e.Text ?? "")
            .SetTag(alert)
            .AddArgument("host", e.NodeId.ToString())
            .AddArgument("metric", TabFor(e.Metric))
            .AddArgument("alert", alert);
        if (e.Tint != "ok")
        {
            builder
                .AddButton(Button("Ack", CloudAlertActions.AckId, e, alert))
                .AddButton(Button("Snooze 1 h", CloudAlertActions.SnoozeId, e, alert));
        }
        Show(builder.BuildNotification());
    }

    public void Withdraw(Guid alertId)
    {
        try { _ = _mgr.RemoveByTagAsync(alertId.ToString()); }
        catch (Exception ex) { log?.Warning($"toast: withdraw failed: {ex.Message}"); }
    }

    public void PostFailure(string text)
        => Show(new AppNotificationBuilder().AddText(text).BuildNotification());

    // A button click sends only its own arguments, so each button repeats the toast arguments.
    private static AppNotificationButton Button(string label, string action, StreamEvent e, string alert)
        => new AppNotificationButton(label)
            .AddArgument("action", action)
            .AddArgument("alert", alert)
            .AddArgument("host", e.NodeId.ToString())
            .AddArgument("metric", TabFor(e.Metric));

    // Metrics that have no tab (reachability, long processes) open the CPU tab.
    private static string TabFor(string? metric) => metric is "cpu" or "mem" or "disk" or "health" ? metric : "cpu";

    private void Show(AppNotification n)
    {
        try { _mgr.Show(n); }
        catch (Exception ex) { log?.Warning($"toast: show failed: {ex.Message}"); }
    }
}
