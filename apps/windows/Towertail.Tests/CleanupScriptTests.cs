using System.Diagnostics;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CleanupScriptTests
{
    private static readonly DateTime Now = new(2026, 10, 2, 9, 30, 0, DateTimeKind.Local);

    private static HashSet<string> Safe(DiskScanResult r)
        => r.Recommendations.Where(x => x.IsSafe).Select(x => x.Id).ToHashSet();

    [Fact]
    public void PowerShellScriptForWindows()
    {
        var result = ScanFixture.Result("windows");
        var script = new CleanupScript(result, Safe(result), Now);

        script.Shell.Should().Be(CleanupShell.PowerShell);
        script.ShellLabel.Should().Be("PowerShell · Windows");
        var text = script.Text;
        text.Should().StartWith("# Towertail cleanup for dev-windows · 2026-10-02 09:30\n");
        text.Should().Contain("& {\n$ErrorActionPreference = 'Stop'\n");
        text.Should().Contain("if ([System.Net.Dns]::GetHostName() -ne 'dev-windows') { throw 'wrong host' }");
        text.Should().EndWith("}\n");
        foreach (var rec in script.Chosen)
        {
            text.Should().Contain($"# {rec.Label} · {DiskSize.Format(rec.Bytes)}\n");
            foreach (var c in rec.Cmd.Pwsh!) text.Should().Contain(c);
        }
        text.Should().NotContain("rm -rf");
    }

    [Fact]
    public void BashScriptForLinuxKeepsListOrderAndNotesSudo()
    {
        var result = ScanFixture.Result("linux");
        var selected = result.Recommendations.Select(r => r.Id).Reverse().ToHashSet();
        var script = new CleanupScript(result, selected, Now);

        script.Shell.Should().Be(CleanupShell.Bash);
        script.ShellLabel.Should().Be("bash · Linux");
        script.Chosen.Select(r => r.Id).Should().Equal(result.Recommendations.Select(r => r.Id));
        script.TotalBytes.Should().Be(result.Recommendations.Sum(r => r.Bytes));
        var text = script.Text;
        text.Should().Contain("(\nset -u\n[ \"$(hostname)\" = 'dev-linux' ] || { echo 'wrong host' >&2; exit 1; }\n");
        text.Should().Contain("# sudo lines ask for your password.");
        text.Should().Contain($"# {result.Recommendations.Count} items · ");
        text.Should().EndWith(")\n");

        var positions = script.Chosen.Select(r => text.IndexOf("# " + r.Label + " · ", StringComparison.Ordinal)).ToList();
        positions.Should().BeInAscendingOrder().And.NotContain(-1);
    }

    [Fact]
    public void MacScriptLabelAndNoElevatedNote()
    {
        var result = ScanFixture.Result("darwin");
        var notElevated = result.Recommendations.Where(r => !r.Elevated).Take(1).Select(r => r.Id).ToHashSet();
        var script = new CleanupScript(result, notElevated, Now);
        script.ShellLabel.Should().Be("bash/zsh · macOS");
        script.Text.Should().Contain("# 1 item · ");
        script.Text.Should().NotContain("sudo lines");
    }

    [Fact]
    public void AdminNoteForElevatedPowerShell()
    {
        var result = ScanFixture.Result("windows");
        var rec = result.Recommendations[0] with { Elevated = true };
        var script = new CleanupScript(result with { Recs = new[] { rec } }, new HashSet<string> { rec.Id }, Now);
        script.Text.Should().Contain("# Lines marked admin need an elevated PowerShell.");
    }

    [Fact]
    public void EmptySelectionIsAValidStub()
    {
        var result = ScanFixture.Result("linux");
        var script = new CleanupScript(result, new HashSet<string>(), Now);
        script.Chosen.Should().BeEmpty();
        script.TotalBytes.Should().Be(0);
        script.Text.Should().Contain("# 0 items · 0 B.");
        script.Text.Should().Contain("# Select items to build the script.");
    }

    [Fact]
    public void QuotesInHostnameAreEscaped()
    {
        var linux = ScanFixture.Result("linux") with { Hostname = "o'brien" };
        new CleanupScript(linux, new HashSet<string>(), Now).Text.Should().Contain("= 'o'\\''brien' ]");
        var win = ScanFixture.Result("windows") with { Hostname = "o'brien" };
        new CleanupScript(win, new HashSet<string>(), Now).Text.Should().Contain("-ne 'o''brien')");
    }

    [Fact]
    public void UserItemsInPowerShell()
    {
        var result = ScanFixture.Result("windows");
        var script = new CleanupScript(result, new HashSet<string>(), Now,
            new[] { "C:\\Users\\dev\\Videos\\Captures", "C:\\Users\\dev\\Gone", "C:\\Users\\dev" });
        script.Mine.Select(i => i.Path).Should().Equal("C:\\Users\\dev\\Videos\\Captures", "C:\\Users\\dev");
        script.Count.Should().Be(2);
        script.TotalBytes.Should().Be(3_000_000_000 + result.Tree.B);
        var text = script.Text;
        text.Should().Contain("# Lines marked admin need an elevated PowerShell.");
        text.Should().Contain("# Added by you · 3.0 GB\nRemove-Item -LiteralPath 'C:\\Users\\dev\\Videos\\Captures' -Recurse -Force\n");
        text.Should().Contain("· admin\nRemove-Item -LiteralPath 'C:\\Users\\dev' -Recurse -Force\n");
        text.IndexOf("Remove-Item", StringComparison.Ordinal).Should().BeLessThan(text.LastIndexOf("}\n", StringComparison.Ordinal));
    }

    [Fact]
    public void UserItemsInBash()
    {
        var result = ScanFixture.Result("linux");
        var script = new CleanupScript(result, new HashSet<string>(), Now,
            new[] { "/home/dev/My Project's \"data\"", "/var/log" });
        script.Mine.Should().HaveCount(2);
        var text = script.Text;
        text.Should().Contain("# Added by you · 100 MB\nrm -rf -- \"$HOME\"/'My Project'\\''s \"data\"'\n");
        text.Should().Contain("# Added by you · 321 MB · may need sudo\nrm -rf -- '/var/log'\n");
        text.Should().Contain("# sudo lines ask for your password.");
        text.Should().EndWith(")\n");
        var (code, _, err) = Run("bash", new[] { "-n" }, text);
        code.Should().Be(0, err);
    }

    // The shells, when this machine has them, check the syntax and the host guard.

    [Theory]
    [InlineData("linux")]
    [InlineData("darwin")]
    public void BashParsesTheScript(string os)
    {
        var result = ScanFixture.Result(os);
        var text = new CleanupScript(result, result.Recommendations.Select(r => r.Id).ToHashSet(), Now).Text;
        var (code, _, err) = Run("bash", new[] { "-n" }, text);
        code.Should().Be(0, err);
    }

    [Fact]
    public void BashGuardStopsOnOtherHost()
    {
        var marker = Path.Combine(Path.GetTempPath(), $"tt-guard-{Guid.NewGuid():N}");
        var result = ScanFixture.Result("linux") with { Hostname = "no-such-host-" + Guid.NewGuid().ToString("N")[..6] };
        var rec = result.Recommendations[0] with { Cmd = new DiskRecCmd(new[] { $"touch '{marker}'" }, null) };
        var text = new CleanupScript(result with { Recs = new[] { rec } }, new HashSet<string> { rec.Id }, Now).Text
                   + $"echo after > '{marker}.after'\n";
        var (code, _, err) = Run("bash", Array.Empty<string>(), text);
        try
        {
            code.Should().Be(0, "the guard exits the subshell, not the shell of the user");
            err.Should().Contain("wrong host");
            File.Exists(marker).Should().BeFalse();
            File.Exists(marker + ".after").Should().BeTrue();
        }
        finally
        {
            File.Delete(marker);
            File.Delete(marker + ".after");
        }
    }

    [Fact]
    public void PowerShellParsesTheScript()
    {
        var result = ScanFixture.Result("windows");
        var text = new CleanupScript(result, result.Recommendations.Select(r => r.Id).ToHashSet(), Now).Text;
        const string check =
            "$e=$null; [void][System.Management.Automation.Language.Parser]::ParseInput([Console]::In.ReadToEnd(), [ref]$null, [ref]$e); " +
            "if ($e.Count) { $e | ForEach-Object { $_.Message }; exit 1 }";
        var (code, output, err) = Run("pwsh", new[] { "-NoProfile", "-NonInteractive", "-Command", check }, text);
        code.Should().Be(0, output + err);
    }

    [Fact]
    public void PowerShellGuardStopsOnOtherHost()
    {
        var marker = Path.Combine(Path.GetTempPath(), $"tt-guard-{Guid.NewGuid():N}");
        var result = ScanFixture.Result("windows") with { Hostname = "no-such-host" };
        var rec = result.Recommendations[0] with { Cmd = new DiskRecCmd(null, new[] { $"Set-Content -Path '{marker}' -Value x" }) };
        var text = new CleanupScript(result with { Recs = new[] { rec } }, new HashSet<string> { rec.Id }, Now).Text;
        var file = marker + ".ps1";
        File.WriteAllText(file, text + $"Set-Content -Path '{marker}.after' -Value x\n");
        var (code, output, err) = Run("pwsh", new[] { "-NoProfile", "-NonInteractive", "-File", file }, "");
        try
        {
            code.Should().NotBe(0);
            (output + err).Should().Contain("wrong host");
            File.Exists(marker).Should().BeFalse();
            File.Exists(marker + ".after").Should().BeFalse("ErrorActionPreference Stop ends the script");
        }
        finally
        {
            File.Delete(marker);
            File.Delete(marker + ".after");
            File.Delete(file);
        }
    }

    private static (int Code, string Out, string Err) Run(string shell, string[] args, string stdin)
    {
        var path = Find(shell);
        if (path is null) Assert.Skip($"{shell} is not on this machine");
        var psi = new ProcessStartInfo(path!)
        {
            RedirectStandardInput = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
        };
        foreach (var a in args) psi.ArgumentList.Add(a);
        using var p = Process.Start(psi)!;
        var outTask = p.StandardOutput.ReadToEndAsync();
        var errTask = p.StandardError.ReadToEndAsync();
        p.StandardInput.Write(stdin);
        p.StandardInput.Close();
        if (!p.WaitForExit(60_000)) { p.Kill(true); throw new TimeoutException($"{shell} did not exit"); }
        return (p.ExitCode, outTask.Result, errTask.Result);
    }

    private static string? Find(string name)
    {
        var exts = OperatingSystem.IsWindows() ? new[] { ".exe", "" } : new[] { "" };
        foreach (var dir in (Environment.GetEnvironmentVariable("PATH") ?? "").Split(Path.PathSeparator))
        foreach (var ext in exts)
        {
            var path = Path.Combine(dir, name + ext);
            if (File.Exists(path)) return path;
        }
        return null;
    }
}
