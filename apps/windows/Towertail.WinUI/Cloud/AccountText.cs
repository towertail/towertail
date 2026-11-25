namespace Towertail.WinUI.Cloud;

/// <summary>The texts and small rules of the Account pane. They match the Mac pane.</summary>
public static class AccountText
{
    public const int CodeLength = 6;

    /// <summary>The digits of a typed or pasted text, at most six: "482 913" gives "482913".</summary>
    public static string Digits(string text)
        => new(text.Where(c => c is >= '0' and <= '9').Take(CodeLength).ToArray());

    /// <summary>"0:42". A part of a second counts as a whole second.</summary>
    public static string Clock(TimeSpan wait)
    {
        var s = (int)Math.Ceiling(Math.Max(0, wait.TotalSeconds));
        return $"{s / 60}:{s % 60:00}";
    }

    public static string ResendText(TimeSpan wait)
        => wait > TimeSpan.Zero ? $"Send a new code ({Clock(wait)})" : "Send a new code";

    public static string PlanText(CloudMe me, CloudBilling? billing)
        => billing?.Plan is { } plan
            ? $"{Capitalize(plan.Id)} plan, {plan.MaxNodes} Cloud hosts"
            : $"{Capitalize(me.Org.Plan)} plan";

    public static string HostsText(CloudBilling billing)
        => $"{billing.Nodes} of {billing.MaxNodes ?? billing.Plan.MaxNodes}";

    public static string SshHostsText(int count) => $"{count} (no limit)";

    public static string SignOutMessage(int hosts, int cloudOnly)
    {
        var lines = new List<string>();
        if (hosts > 0)
        {
            var one = hosts == 1;
            lines.Add($"{Count(hosts)} now {(one ? "reports" : "report")} to Cloud. "
                + $"The app removes {(one ? "its sampler" : "their samplers")} and deletes {(one ? "it" : "them")} from Cloud. "
                + $"This PC then watches {(one ? "it" : "them")} over SSH again.");
            lines.Add($"Cloud alerts for {(one ? "this host" : "these hosts")} stop.");
        }
        else
        {
            lines.Add("No host on this PC reports to Cloud.");
        }
        if (cloudOnly > 0)
        {
            var one = cloudOnly == 1;
            lines.Add($"{Count(cloudOnly)} {(one ? "exists" : "exist")} only in Cloud. "
                + $"{(one ? "It stays" : "They stay")} in Cloud and {(one ? "leaves" : "leave")} this PC.");
        }
        return string.Join(' ', lines);
    }

    public static string LeavingText(int hosts)
        => hosts == 1 ? "The app moves 1 host back to SSH." : $"The app moves {hosts} hosts back to SSH.";

    public static string DeleteDetail(CloudOrgDeletion org) => org.Fate switch
    {
        "delete" => org.Nodes == 1 ? "Deleted with 1 host" : $"Deleted with {org.Nodes} hosts",
        "leave" => "You leave. The other members stay.",
        _ => org.Reason ?? "Cloud blocks the delete.",
    };

    public static string DeleteBadge(CloudOrgDeletion org) => org.Fate switch
    {
        "delete" => "Deleted",
        "leave" => "You leave",
        _ => "Blocked",
    };

    public static bool CanDelete(CloudDeletion? deletion, string typed, bool busy)
        => deletion is { Blocked: false } && !busy
            && string.Equals(typed.Trim(), deletion.Email, StringComparison.OrdinalIgnoreCase);

    private static string Count(int hosts) => hosts == 1 ? "1 host" : $"{hosts} hosts";

    private static string Capitalize(string s) => s.Length == 0 ? s : char.ToUpperInvariant(s[0]) + s[1..].ToLowerInvariant();
}
