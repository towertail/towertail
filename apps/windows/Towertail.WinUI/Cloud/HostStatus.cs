using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

public enum HostStatusLevel { Online, Offline, Disabled, Pending }

/// <summary>The state of a host in the Servers list.</summary>
public readonly record struct HostStatus(string Text, HostStatusLevel Level)
{
    /// <summary>
    /// An open reachability alert means a cloud host is offline, whatever its last summary says. Other
    /// hosts that reported (a poll or a summary) are online, or offline with a reason. A cloud host
    /// with no summary yet takes its state from the feed.
    /// </summary>
    public static HostStatus For(Node node, ServerViewModel? vm, bool feedConnected, bool alerted)
    {
        if (!node.Enabled) return new("disabled", HostStatusLevel.Disabled);
        if (node.IsCloud && alerted) return new("offline", HostStatusLevel.Offline);
        if (vm is null) return new("—", HostStatusLevel.Pending);
        if (vm.OfflineReason is { Length: > 0 }) return new("offline", HostStatusLevel.Offline);
        if (vm.LastSeen is not null) return new("online", HostStatusLevel.Online);
        if (!node.IsCloud) return new("…", HostStatusLevel.Pending);
        return feedConnected ? new("online", HostStatusLevel.Online) : new("connecting", HostStatusLevel.Pending);
    }
}
