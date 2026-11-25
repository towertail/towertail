namespace Towertail.WinUI.State;

/// <summary>The small tag after the host name, on a card and on a row.</summary>
public enum HostTag { Local, Cloud, CloudOffline, SshOnly, OfflineAlerted }

public static class HostTags
{
    /// <summary>
    /// Null when no tag shows. An SSH host gets a tag only when the user is signed in:
    /// with no account each host is SSH, so the tag tells nothing.
    /// </summary>
    public static HostTag? For(Node node, bool signedIn, bool feedConnected, bool alerted)
    {
        if (node.Kind == NodeKind.Local) return HostTag.Local;
        if (node.IsCloud)
        {
            if (alerted) return HostTag.OfflineAlerted;
            return feedConnected ? HostTag.Cloud : HostTag.CloudOffline;
        }
        return signedIn ? HostTag.SshOnly : null;
    }

    public static string Label(this HostTag tag) => tag switch
    {
        HostTag.Local => "LOCAL",
        HostTag.Cloud => "CLOUD",
        HostTag.CloudOffline => "CLOUD OFFLINE",
        HostTag.SshOnly => "SSH ONLY",
        HostTag.OfflineAlerted => "OFFLINE · ALERTED",
        _ => "",
    };
}
