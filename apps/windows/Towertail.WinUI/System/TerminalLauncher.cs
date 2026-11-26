using System.Diagnostics;
using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Detects the user's preferred terminal and invokes the ssh command of the node inside it.
/// Priority: Windows Terminal → PowerShell 7 → cmd → Alacritty → WezTerm → Tabby.
/// </summary>
public static class TerminalLauncher
{
    internal sealed record Terminal(string Exe, string ArgsFormat, bool AlwaysPresent = false, bool UseShellExecute = false)
    {
        public bool Exists() => AlwaysPresent || ResolveOnPath(Exe) != null;

        public ProcessStartInfo StartInfo(string sshCommand)
            => new(Exe, string.Format(ArgsFormat, sshCommand)) { UseShellExecute = UseShellExecute };
    }

    internal static readonly Terminal[] Terminals =
    {
        new("wt.exe", "{0}"),
        new("pwsh.exe", "-NoExit -Command {0}"),
        new("cmd.exe", "/k {0}", AlwaysPresent: true, UseShellExecute: true),
        new("alacritty.exe", "-e {0}"),
        new("wezterm-gui.exe", "start -- {0}"),
        new("tabby.exe", "{0}"),
    };

    public static bool OpenSsh(Node node)
    {
        if (node.Kind != NodeKind.Ssh) return false;
        var command = node.SshCommand;
        foreach (var terminal in Terminals)
        {
            if (!terminal.Exists()) continue;
            try
            {
                Process.Start(terminal.StartInfo(command));
                return true;
            }
            catch { /* try next */ }
        }
        return false;
    }

    private static string? ResolveOnPath(string exe)
    {
        var path = Environment.GetEnvironmentVariable("PATH") ?? "";
        foreach (var dir in path.Split(Path.PathSeparator, StringSplitOptions.RemoveEmptyEntries))
        {
            try
            {
                var candidate = Path.Combine(dir, exe);
                if (File.Exists(candidate)) return candidate;
            }
            catch { }
        }
        return null;
    }
}
