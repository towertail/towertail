using System.Diagnostics;
using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Detects the user's preferred terminal and invokes the ssh command of the node inside it.
/// Priority: Windows Terminal → PowerShell 7 → cmd → Alacritty → WezTerm → Tabby.
/// </summary>
public static class TerminalLauncher
{
    public static bool OpenSsh(Node node)
    {
        if (node.Kind != NodeKind.Ssh) return false;
        var command = node.SshCommand;
        foreach (var candidate in Candidates())
        {
            if (!candidate.Exists()) continue;
            try
            {
                candidate.Launch(command);
                return true;
            }
            catch { /* try next */ }
        }
        return false;
    }

    private interface ITerminal
    {
        bool Exists();
        void Launch(string sshCommand);
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
        public void Launch(string c) => Process.Start("wt.exe", c);
    }
    private sealed class Pwsh : ITerminal
    {
        public bool Exists() => ResolveOnPath("pwsh.exe") != null;
        public void Launch(string c) => Process.Start("pwsh.exe", $"-NoExit -Command {c}");
    }
    private sealed class Cmd : ITerminal
    {
        public bool Exists() => true;
        public void Launch(string c) => Process.Start(new ProcessStartInfo("cmd.exe", $"/k {c}") { UseShellExecute = true });
    }
    private sealed class Alacritty : ITerminal
    {
        public bool Exists() => ResolveOnPath("alacritty.exe") != null;
        public void Launch(string c) => Process.Start("alacritty.exe", $"-e {c}");
    }
    private sealed class WezTerm : ITerminal
    {
        public bool Exists() => ResolveOnPath("wezterm-gui.exe") != null;
        public void Launch(string c) => Process.Start("wezterm-gui.exe", $"start -- {c}");
    }
    private sealed class Tabby : ITerminal
    {
        public bool Exists() => ResolveOnPath("tabby.exe") != null;
        public void Launch(string c) => Process.Start("tabby.exe", c);
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
