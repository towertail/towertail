using System.Globalization;
using System.Text;

namespace Towertail.WinUI.State;

public enum CleanupShell { Bash, PowerShell }

/// <summary>
/// The cleanup script for the selected recommendations of a scan. Towertail shows it and
/// copies it. It never runs it. The body is in a block (<c>( … )</c> for bash,
/// <c>&amp; { … }</c> for PowerShell), so that a pasted script stops as one unit
/// at the hostname guard and its settings do not stay in the shell of the user.
/// </summary>
public sealed class CleanupScript
{
    public DiskScanResult Result { get; }
    /// <summary>The selected recommendations, in list order.</summary>
    public IReadOnlyList<DiskRec> Chosen { get; }
    public CleanupShell Shell { get; }
    public long TotalBytes { get; }
    public string Text { get; }

    public CleanupScript(DiskScanResult result, IReadOnlySet<string> selected, DateTime now)
    {
        Result = result;
        Shell = result.IsWindows ? CleanupShell.PowerShell : CleanupShell.Bash;
        Chosen = result.Recommendations.Where(r => selected.Contains(r.Id)).ToList();
        TotalBytes = Chosen.Sum(r => r.Bytes);
        Text = Build(now);
    }

    public string ShellLabel => Result.Os switch
    {
        "windows" => "PowerShell · Windows",
        "darwin" => "bash/zsh · macOS",
        _ => "bash · Linux",
    };

    private string Build(DateTime now)
    {
        var sb = new StringBuilder();
        var stamp = now.ToLocalTime().ToString("yyyy-MM-dd HH:mm", CultureInfo.InvariantCulture);
        var ps = Shell == CleanupShell.PowerShell;
        var host = Result.Hostname.Replace("'", ps ? "''" : "'\\''");

        sb.Append("# Towertail cleanup for ").Append(Result.Hostname).Append(" · ").Append(stamp).Append('\n');
        sb.Append("# ").Append(Chosen.Count).Append(Chosen.Count == 1 ? " item · " : " items · ")
          .Append(DiskSize.Format(TotalBytes)).Append(". Read each line before you run it.\n");
        if (Chosen.Any(r => r.Elevated))
            sb.Append(ps ? "# Lines marked admin need an elevated PowerShell.\n" : "# sudo lines ask for your password.\n");

        if (ps)
        {
            sb.Append("& {\n");
            sb.Append("$ErrorActionPreference = 'Stop'\n");
            // The DNS host name, as the sampler reports it. COMPUTERNAME is cut to 15 characters.
            sb.Append("if ([System.Net.Dns]::GetHostName() -ne '").Append(host).Append("') { throw 'wrong host' }\n");
        }
        else
        {
            sb.Append("(\n");
            sb.Append("set -u\n");
            sb.Append("[ \"$(hostname)\" = '").Append(host).Append("' ] || { echo 'wrong host' >&2; exit 1; }\n");
        }
        sb.Append('\n');

        if (Chosen.Count == 0) sb.Append("# Select items to build the script.\n\n");
        foreach (var r in Chosen)
        {
            sb.Append("# ").Append(r.Label).Append(" · ").Append(DiskSize.Format(r.Bytes)).Append('\n');
            foreach (var c in (ps ? r.Cmd.Pwsh : r.Cmd.Sh) ?? Array.Empty<string>())
                sb.Append(c).Append('\n');
            sb.Append('\n');
        }
        sb.Append(ps ? "}\n" : ")\n");
        return sb.ToString();
    }
}
