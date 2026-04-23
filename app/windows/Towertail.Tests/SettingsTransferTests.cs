using FluentAssertions;
using System.Text.Json;
using System.Text.Json.Nodes;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class SettingsTransferTests
{
    [Fact]
    public void LegacyMacFileMigratesIntoPlatformDarwin()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.Copy(Path.Combine("TestData", "settings.mac.json"), path, overwrite: true);

            var loaded = SettingsPersistence.Load(path);
            loaded.Platform.Darwin.LaunchAtLogin.Should().BeTrue();
            loaded.Platform.Darwin.DefaultTerminalApp.Should().Be("iTerm");
            loaded.Platform.Windows.LaunchAtStartup.Should().BeFalse();
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void RoundTripPreservesDarwinBlock()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.Copy(Path.Combine("TestData", "settings.mac.json"), path, overwrite: true);
            var loaded = SettingsPersistence.Load(path);
            SettingsPersistence.Save(loaded, path);

            var json = File.ReadAllText(path);
            var root = JsonNode.Parse(json)!.AsObject();
            root["platform"]!["darwin"]!["launchAtLogin"]!.GetValue<bool>().Should().BeTrue();
            root["platform"]!["darwin"]!["defaultTerminalApp"]!.GetValue<string>().Should().Be("iTerm");
            root["platform"]!["windows"].Should().NotBeNull();
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void BuildFromCurrentReflectsPersistedState()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.Copy(Path.Combine("TestData", "settings.mac.json"), path, overwrite: true);
            var export = SettingsTransfer.BuildFromCurrent(path);
            export.Nodes.Should().HaveCount(1);
            export.Nodes[0].DisplayName.Should().Be("mbp.local");
            export.GlobalThresholds.CpuWarn.Should().BeApproximately(0.75, 0.0001);
        }
        finally { File.Delete(path); }
    }
}
