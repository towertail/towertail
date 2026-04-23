using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Cross-OS settings schema. Mirrors the Mac PersistedSettings struct but adds a top-level
/// <c>platform</c> envelope so each OS reads only its own sub-object and preserves the others
/// on save. Missing envelope (legacy Mac file) → migrate known flat fields into platform.darwin.
/// </summary>
public sealed class PersistedSettings
{
    public int SchemaVersion { get; set; } = 1;
    public List<Node> Nodes { get; set; } = new() { Node.LocalWindows() };
    public PersistedThresholds Thresholds { get; set; } = PersistedThresholds.Defaults;
    public int LocalPollingIntervalSeconds { get; set; } = 2;
    public int SshPollingIntervalSeconds { get; set; } = 10;
    public string CardDensity { get; set; } = "a";
    public bool NotificationsEnabled { get; set; }
    public bool NotifyWarn { get; set; } = true;
    public bool NotifyCritical { get; set; } = true;
    public int NotifyDebounceSeconds { get; set; } = 60;
    public bool AutoUpdateSamplersEnabled { get; set; }
    public int PostWakeGraceSeconds { get; set; } = 15;

    /// <summary>
    /// Per-OS sub-objects. We read our own (<c>windows</c>) and preserve all others verbatim
    /// so Mac <-> Windows round-trips don't clobber Mac-only fields.
    /// </summary>
    public PlatformEnvelope Platform { get; set; } = new();

    public static PersistedSettings Defaults() => new();
}

public sealed class PlatformEnvelope
{
    public DarwinPlatform Darwin { get; set; } = new();
    public WindowsPlatform Windows { get; set; } = new();
    /// <summary>Unknown / future OS blocks — carried through verbatim on save.</summary>
    [JsonExtensionData]
    public Dictionary<string, JsonElement> Unknown { get; set; } = new();
}

public sealed class DarwinPlatform
{
    public bool LaunchAtLogin { get; set; }
    public string DefaultTerminalApp { get; set; } = "Terminal";
}

public sealed class WindowsPlatform
{
    public bool LaunchAtStartup { get; set; }
    public string DefaultTerminalApp { get; set; } = "WindowsTerminal";
}

public sealed class PersistedThresholds
{
    public double CpuWarn { get; set; } = 0.75;
    public double CpuCritical { get; set; } = 0.90;
    public double MemWarn { get; set; } = 0.75;
    public double MemCritical { get; set; } = 0.90;
    public double DiskWarn { get; set; } = 0.85;
    public double DiskCritical { get; set; } = 0.95;

    public static PersistedThresholds Defaults => new();
}

public static class SettingsPersistence
{
    public static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true,
        DefaultIgnoreCondition = JsonIgnoreCondition.Never,
        // Accept unknown fields on load (future-proof), preserve order.
        AllowTrailingCommas = true,
        ReadCommentHandling = JsonCommentHandling.Skip,
    };

    public static string DefaultPath()
    {
        var appData = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
        var dir = Path.Combine(appData, "Towertail");
        Directory.CreateDirectory(dir);
        return Path.Combine(dir, "settings.json");
    }

    public static PersistedSettings Load(string path)
    {
        if (!File.Exists(path)) return PersistedSettings.Defaults();

        try
        {
            var text = File.ReadAllText(path);
            if (string.IsNullOrWhiteSpace(text)) return PersistedSettings.Defaults();

            // Parse as JsonNode first so we can migrate legacy flat Mac files
            // (missing "platform" envelope) without tripping System.Text.Json's
            // strict binding and without losing unknown future keys.
            var root = JsonNode.Parse(text) as JsonObject
                ?? throw new JsonException("settings root is not a JSON object");

            MigrateLegacyPlatform(root);

            var settings = root.Deserialize<PersistedSettings>(JsonOptions)
                ?? PersistedSettings.Defaults();

            if (settings.Nodes.Count == 0)
                settings.Nodes.Add(Node.LocalWindows());
            return settings;
        }
        catch
        {
            return PersistedSettings.Defaults();
        }
    }

    /// <summary>
    /// When a file from a legacy Mac build (pre-platform-envelope) is loaded, it has
    /// <c>launchAtLogin</c> + <c>defaultTerminalApp</c> at the top level and no
    /// <c>platform</c> key. We migrate those into <c>platform.darwin</c> so this
    /// loader — and any subsequent save — emits the normalized form without
    /// dropping the user's existing preferences.
    /// </summary>
    private static void MigrateLegacyPlatform(JsonObject root)
    {
        if (root.ContainsKey("platform")) return;

        var darwin = new JsonObject();
        if (root["launchAtLogin"] is JsonNode launch)
            darwin["launchAtLogin"] = launch.DeepClone();
        else
            darwin["launchAtLogin"] = false;

        if (root["defaultTerminalApp"] is JsonNode term)
            darwin["defaultTerminalApp"] = term.DeepClone();
        else
            darwin["defaultTerminalApp"] = "Terminal";

        root["platform"] = new JsonObject
        {
            ["darwin"] = darwin,
            ["windows"] = new JsonObject
            {
                ["launchAtStartup"] = false,
                ["defaultTerminalApp"] = "WindowsTerminal",
            },
        };

        // Leave the legacy flat keys in place on the node so a round-trip
        // through an older Mac binary still works; the Mac app v1.1 will rely
        // on the envelope but the older versions still read the flat form.
    }

    public static bool Save(PersistedSettings settings, string path)
    {
        try
        {
            var dir = Path.GetDirectoryName(path);
            if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);

            // Build the object via JsonNode so we can also emit legacy flat
            // keys mirroring the darwin platform block — keeps older Mac
            // builds readable until v1.1 ships.
            var node = JsonSerializer.SerializeToNode(settings, JsonOptions) as JsonObject
                ?? throw new JsonException("failed to serialize settings");

            if (node["platform"] is JsonObject platform && platform["darwin"] is JsonObject dd)
            {
                if (dd["launchAtLogin"] is JsonNode l) node["launchAtLogin"] = l.DeepClone();
                if (dd["defaultTerminalApp"] is JsonNode t) node["defaultTerminalApp"] = t.DeepClone();
            }

            // Atomic write: write to temp, then move.
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, node.ToJsonString(JsonOptions));
            if (File.Exists(path)) File.Replace(tmp, path, null);
            else File.Move(tmp, path);
            return true;
        }
        catch
        {
            return false;
        }
    }
}
