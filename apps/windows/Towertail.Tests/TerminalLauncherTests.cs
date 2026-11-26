using FluentAssertions;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class TerminalLauncherTests
{
    private const string Ssh = "ssh -p 2222 a@h";

    [Theory]
    [InlineData("wt.exe", "ssh -p 2222 a@h", false)]
    [InlineData("pwsh.exe", "-NoExit -Command ssh -p 2222 a@h", false)]
    [InlineData("cmd.exe", "/k ssh -p 2222 a@h", true)]
    [InlineData("alacritty.exe", "-e ssh -p 2222 a@h", false)]
    [InlineData("wezterm-gui.exe", "start -- ssh -p 2222 a@h", false)]
    [InlineData("tabby.exe", "ssh -p 2222 a@h", false)]
    public void StartInfoMatchesTerminal(string exe, string args, bool shellExecute)
    {
        var info = TerminalLauncher.Terminals.Single(t => t.Exe == exe).StartInfo(Ssh);
        info.FileName.Should().Be(exe);
        info.Arguments.Should().Be(args);
        info.UseShellExecute.Should().Be(shellExecute);
        TerminalLauncher.Terminals.Select(t => t.Exe).Should().Equal(
            "wt.exe", "pwsh.exe", "cmd.exe", "alacritty.exe", "wezterm-gui.exe", "tabby.exe");
    }
}
