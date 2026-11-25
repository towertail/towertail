using Towertail.WinUI.Backend;

namespace Towertail.WinUI.Cloud;

/// <summary>The toast calls of <see cref="CloudFeed"/> and <see cref="CloudSession"/>. A fake replaces it in tests.</summary>
public interface ICloudAlertPoster
{
    void Post(StreamEvent e, string hostName);
    void Withdraw(Guid alertId);
    void PostFailure(string text);
}

/// <summary>A button of the toast of a cloud alert.</summary>
public enum CloudAlertAction { Ack, Snooze }

public static class CloudAlertActions
{
    public const string AckId = "ack";
    public const string SnoozeId = "snooze";

    /// <summary>The time that "Snooze 1 h" adds.</summary>
    public static readonly TimeSpan SnoozeSpan = TimeSpan.FromHours(1);

    public static string Id(this CloudAlertAction action) => action == CloudAlertAction.Ack ? AckId : SnoozeId;

    /// <summary>Maps the <c>action</c> argument of a toast. Other values (a tap) give null.</summary>
    public static CloudAlertAction? Parse(string? id) => id switch
    {
        AckId => CloudAlertAction.Ack,
        SnoozeId => CloudAlertAction.Snooze,
        _ => null,
    };
}
