using System.Diagnostics;
using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Detects the user's preferred terminal and invokes <c>ssh user@host</c> inside it.
/// Priority: Windows Terminal → PowerShell 7 → cmd → Alacritty → WezTerm → Tabby.
/// </summary>
public static class TerminalLauncher
{
    public static bool OpenSsh(Node node)
    {
        if (node.Kind != NodeKind.Ssh) return false;
        var target = node.UserAtHost;
        foreach (var candidate in Candidates())
        {
            if (!candidate.Exists()) continue;
            try
            {
                candidate.Launch(target);
                return true;
            }
            catch { /* try next */ }
        }
        return false;
    }

    private interface ITerminal
    {
        bool Exists();
        void Launch(string userAtHost);
    }

    private static IEnumerable<ITerminal> Candidates()
    {
        yield return new WindowsTerminal();
        yield return new Pwsh();
        yield return new Cmd();
        yield return new Alacritty();
        yield return new WezTerm();
        yield return new Tabby();
    }

    private sealed class WindowsTerminal : ITerminal
    {
        public bool Exists() => ResolveOnPath("wt.exe") != null;
        public void Launch(string u) => Process.Start("wt.exe", $"ssh {u}");
    }
    private sealed class Pwsh : ITerminal
    {
        public bool Exists() => ResolveOnPath("pwsh.exe") != null;
        public void Launch(string u) => Process.Start("pwsh.exe", $"-NoExit -Command ssh {u}");
    }
    private sealed class Cmd : ITerminal
    {
        public bool Exists() => true;
        public void Launch(string u) => Process.Start(new ProcessStartInfo("cmd.exe", $"/k ssh {u}") { UseShellExecute = true });
    }
    private sealed class Alacritty : ITerminal
    {
        public bool Exists() => ResolveOnPath("alacritty.exe") != null;
        public void Launch(string u) => Process.Start("alacritty.exe", $"-e ssh {u}");
    }
    private sealed class WezTerm : ITerminal
    {
        public bool Exists() => ResolveOnPath("wezterm-gui.exe") != null;
        public void Launch(string u) => Process.Start("wezterm-gui.exe", $"start -- ssh {u}");
    }
    private sealed class Tabby : ITerminal
    {
        public bool Exists() => ResolveOnPath("tabby.exe") != null;
        public void Launch(string u) => Process.Start("tabby.exe", $"ssh {u}");
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
