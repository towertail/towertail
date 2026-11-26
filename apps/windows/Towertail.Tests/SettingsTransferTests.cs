using FluentAssertions;
using System.Text.Json;
using System.Text.Json.Nodes;
using Towertail.WinUI.State;
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

    [Fact]
    public void TrayIconStylePersistsInWindowsBlock()
    {
        var path = Path.GetTempFileName();
        try
        {
            new ClientSettings(path).TrayIconStyle.Should().Be(TrayIconStyle.Pulse);

            var settings = new ClientSettings(path) { TrayIconStyle = TrayIconStyle.Full };
            settings.Persist();

            var root = JsonNode.Parse(File.ReadAllText(path))!.AsObject();
            root["platform"]!["windows"]!["trayIconStyle"]!.GetValue<string>().Should().Be("full");
            new ClientSettings(path).TrayIconStyle.Should().Be(TrayIconStyle.Full);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ExportCarriesTrayIconStyle()
    {
        var path = Path.GetTempFileName();
        try
        {
            new ClientSettings(path) { TrayIconStyle = TrayIconStyle.Full }.Persist();
            var export = SettingsTransfer.BuildFromCurrent(path);
            export.General.MenuBarIconStyle.Should().Be("full");

            var options = new SettingsImportOptions(true, false, false, false, false);
            export.General.MenuBarIconStyle = null;
            SettingsTransfer.Apply(export, options, path);
            SettingsPersistence.Load(path).Platform.Windows.TrayIconStyle.Should().Be("full");

            export.General.MenuBarIconStyle = "pulse";
            SettingsTransfer.Apply(export, options, path);
            SettingsPersistence.Load(path).Platform.Windows.TrayIconStyle.Should().Be("pulse");
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ImportsMacFileWithHealthLimitsAndPerMetricOverrides()
    {
        var src = Path.GetTempFileName();
        var dst = Path.GetTempFileName();
        try
        {
            File.WriteAllText(src, """
            {"schemaVersion":1,
             "nodes":[{"id":"1e9e5d70-9aa3-4c8f-9ff4-7b7b9c5d8e2a","displayName":"db","kind":"ssh",
               "thresholdOverrides":{"mem":{"warn":0.6,"critical":0.7},"zombies":{"warn":5,"critical":9},"memPressure":{"warn":50,"critical":60}}}],
             "thresholds":{"cpuWarn":0.75,"cpuCritical":0.9,"memWarn":0.75,"memCritical":0.9,"diskWarn":0.85,"diskCritical":0.95,
               "procsWarn":100,"procsCritical":200,"zombiesWarn":3,"zombiesCritical":4,
               "health":{"pids":{"warn":0.5,"critical":0.6},"macMemPressure":{"warn":70,"critical":85}}}}
            """);
            var p = SettingsPersistence.Load(src);
            var export = new SettingsExport { GlobalThresholds = p.Thresholds, Nodes = p.Nodes };
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, true, true), dst);

            var settings = new ServerSettings(dst);
            var h = settings.Thresholds.HealthOrDefault;
            h.Pids.Should().Be(new ThresholdPair(0.5, 0.6));
            h.MacMemPressure.Should().Be(new ThresholdPair(70, 85));
            h.Files.Should().Be(HealthLimits.Defaults.Files);

            var node = SettingsPersistence.Load(dst).Nodes.Single(n => n.DisplayName == "db");
            node.ThresholdOverrides.Should().Be(new ThresholdOverrides(
                Mem: new ThresholdPair(0.6, 0.7), Zombies: new ThresholdPair(5, 9), MemPressure: new ThresholdPair(50, 60)));
            MetricThresholds.Effective(settings.Thresholds, node.ThresholdOverrides).HealthOrDefault.MacMemPressure
                .Should().Be(new ThresholdPair(50, 60));
        }
        finally { File.Delete(src); File.Delete(dst); }
    }

    [Fact]
    public void PersistKeepsHealthLimits()
    {
        var path = Path.GetTempFileName();
        try
        {
            var limits = HealthLimits.Defaults with { Inodes = new ThresholdPair(0.4, 0.5), IoPressure = new ThresholdPair(1, 2) };
            var s = new ServerSettings(path);
            s.Thresholds = s.Thresholds with { Health = limits };
            s.Persist();

            new ServerSettings(path).Thresholds.HealthOrDefault.Should().Be(limits);
            var root = JsonNode.Parse(File.ReadAllText(path))!.AsObject();
            root["thresholds"]!["health"]!["inodes"]!["warn"]!.GetValue<double>().Should().Be(0.4);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ImportWithoutSectionsKeepsCurrentValues()
    {
        var path = Path.GetTempFileName();
        try
        {
            var p = PersistedSettings.Defaults();
            var muted = new MutedSignals(new[] { AlertSignal.Disk });
            var alerts = AlertRules.Defaults with { Disk = new AlertRule(60, AlertNotify.Off) };
            p.Thresholds.Muted = muted;
            p.Thresholds.Alerts = alerts;
            p.LongProc = new LongProcRule { CpuPct = 80 };
            SettingsPersistence.Save(p, path);

            var export = SettingsTransfer.Decode("""{"globalThresholds": {"cpuWarn": 0.5}}""");
            export.GlobalThresholds.Muted.Should().BeNull();
            export.LongProc.Should().BeNull();
            SettingsTransfer.Apply(export, new SettingsImportOptions(false, true, false, false, false), path);

            var back = SettingsPersistence.Load(path);
            back.Thresholds.CpuWarn.Should().Be(0.5);
            back.Thresholds.Muted.Should().Be(muted);
            back.Thresholds.Alerts.Should().Be(alerts);
            back.LongProc.CpuPct.Should().Be(80);
        }
        finally { File.Delete(path); }
    }
}
