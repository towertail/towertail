using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Import/export envelope for moving settings between machines. JSON shape identical to
/// Mac's SettingsTransfer.swift: general / globalThresholds / notifications / nodes, plus a
/// <c>sourcePlatform</c> discriminator so the import sheet can warn about OS-specific fields.
/// </summary>
public sealed class SettingsExport
{
    public int Version { get; set; } = 1;
    public DateTime ExportedAt { get; set; } = DateTime.UtcNow;
    public string AppVersion { get; set; } = "0.1.0";
    public string SourcePlatform { get; set; } = "windows";
    public GeneralBlock General { get; set; } = new();
    public PersistedThresholds GlobalThresholds { get; set; } = PersistedThresholds.Defaults;
    public NotificationsBlock Notifications { get; set; } = new();
    public List<Node> Nodes { get; set; } = new();

    /// <summary>
    /// Foreign (non-current-OS) platform blocks carried through on import so a round-trip
    /// doesn't clobber Mac-only fields on a Mac file imported on Windows.
    /// </summary>
    [JsonExtensionData]
    public Dictionary<string, JsonElement> Unknown { get; set; } = new();

    public sealed class GeneralBlock
    {
        public int LocalPollingIntervalSeconds { get; set; } = 2;
        public int SshPollingIntervalSeconds { get; set; } = 10;
        public string CardDensity { get; set; } = "a";
        public int PostWakeGraceSeconds { get; set; } = 15;
        public bool AutoUpdateSamplersEnabled { get; set; }
    }

    public sealed class NotificationsBlock
    {
        public bool NotificationsEnabled { get; set; }
        public bool NotifyWarn { get; set; } = true;
        public bool NotifyCritical { get; set; } = true;
        public int NotifyDebounceSeconds { get; set; } = 60;
    }

    public static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true,
        DefaultIgnoreCondition = JsonIgnoreCondition.Never,
    };
}

/// <summary>
/// Flags the user chose in the pre-import matrix (general / thresholds / notifications /
/// servers × merge|overwrite). The apply pass turns these into mutations on the live stores.
/// </summary>
public sealed record SettingsImportOptions(
    bool ImportGeneral,
    bool ImportThresholds,
    bool ImportNotifications,
    bool ImportNodes,
    bool OverwriteNodes);

public static class SettingsTransfer
{
    public static string Encode(SettingsExport export)
        => JsonSerializer.Serialize(export, SettingsExport.JsonOptions);

    public static SettingsExport Decode(string json)
        => JsonSerializer.Deserialize<SettingsExport>(json, SettingsExport.JsonOptions)
           ?? throw new JsonException("empty export");

    public static SettingsExport BuildFromCurrent(string path)
    {
        var p = SettingsPersistence.Load(path);
        return new SettingsExport
        {
            Version = 1,
            ExportedAt = DateTime.UtcNow,
            SourcePlatform = "windows",
            General = new SettingsExport.GeneralBlock
            {
                LocalPollingIntervalSeconds = p.LocalPollingIntervalSeconds,
                SshPollingIntervalSeconds = p.SshPollingIntervalSeconds,
                CardDensity = p.CardDensity,
                PostWakeGraceSeconds = p.PostWakeGraceSeconds,
                AutoUpdateSamplersEnabled = p.AutoUpdateSamplersEnabled,
            },
            GlobalThresholds = p.Thresholds,
            Notifications = new SettingsExport.NotificationsBlock
            {
                NotificationsEnabled = p.NotificationsEnabled,
                NotifyWarn = p.NotifyWarn,
                NotifyCritical = p.NotifyCritical,
                NotifyDebounceSeconds = p.NotifyDebounceSeconds,
            },
            Nodes = p.Nodes,
        };
    }

    public static void Apply(SettingsExport export, SettingsImportOptions options, string path)
    {
        var p = SettingsPersistence.Load(path);

        if (options.ImportGeneral)
        {
            p.LocalPollingIntervalSeconds = export.General.LocalPollingIntervalSeconds;
            p.SshPollingIntervalSeconds = export.General.SshPollingIntervalSeconds;
            p.CardDensity = export.General.CardDensity;
            p.PostWakeGraceSeconds = export.General.PostWakeGraceSeconds;
            p.AutoUpdateSamplersEnabled = export.General.AutoUpdateSamplersEnabled;
        }
        if (options.ImportThresholds) p.Thresholds = export.GlobalThresholds;
        if (options.ImportNotifications)
        {
            p.NotificationsEnabled = export.Notifications.NotificationsEnabled;
            p.NotifyWarn = export.Notifications.NotifyWarn;
            p.NotifyCritical = export.Notifications.NotifyCritical;
            p.NotifyDebounceSeconds = export.Notifications.NotifyDebounceSeconds;
        }
        if (options.ImportNodes)
        {
            if (options.OverwriteNodes) p.Nodes = export.Nodes.ToList();
            else
            {
                var existingIds = p.Nodes.Select(n => n.Id).ToHashSet();
                foreach (var n in export.Nodes)
                    if (!existingIds.Contains(n.Id)) p.Nodes.Add(n);
            }
        }
        SettingsPersistence.Save(p, path);
    }
}
