using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// DPAPI-backed password store, keyed by node id. Lives in a sibling
/// <c>credentials.json</c> next to <c>settings.json</c> — kept separate so
/// machine-bound ciphertext never ends up in the cross-platform-synced
/// settings file (and so SettingsTransfer's export path can trivially exclude it).
/// <para>
/// Ciphertext is produced by <see cref="ProtectedData.Protect(byte[], byte[], DataProtectionScope)"/>
/// with <see cref="DataProtectionScope.CurrentUser"/>, so the entry is only
/// decryptable by this Windows user on this machine — exactly what we want.
/// </para>
/// </summary>
public static class DpapiStore
{
    /// <summary>File name, resolved against the same directory as settings.json.</summary>
    public const string FileName = "credentials.json";

    private static readonly byte[] s_entropy = Encoding.UTF8.GetBytes("com.towertail.ssh");

    /// <summary>Default location: sibling of <c>%APPDATA%\Towertail\settings.json</c>.</summary>
    public static string DefaultPath()
    {
        var appData = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
        var dir = Path.Combine(appData, "Towertail");
        Directory.CreateDirectory(dir);
        return Path.Combine(dir, FileName);
    }

    public static void SetPassword(Guid nodeId, string plaintext, string? path = null)
    {
        var file = path ?? DefaultPath();
        var store = Load(file);
        var cipher = ProtectedData.Protect(
            Encoding.UTF8.GetBytes(plaintext),
            s_entropy,
            DataProtectionScope.CurrentUser);
        store[nodeId.ToString("D")] = Convert.ToBase64String(cipher);
        Save(file, store);
    }

    public static string? GetPassword(Guid nodeId, string? path = null)
    {
        var file = path ?? DefaultPath();
        var store = Load(file);
        if (!store.TryGetValue(nodeId.ToString("D"), out var b64) || string.IsNullOrEmpty(b64))
            return null;
        try
        {
            var cipher = Convert.FromBase64String(b64);
            var plain = ProtectedData.Unprotect(cipher, s_entropy, DataProtectionScope.CurrentUser);
            return Encoding.UTF8.GetString(plain);
        }
        catch
        {
            // Ciphertext written by a different user or on a different machine
            // is unrecoverable — surface as "no password" rather than throwing.
            return null;
        }
    }

    public static void DeletePassword(Guid nodeId, string? path = null)
    {
        var file = path ?? DefaultPath();
        var store = Load(file);
        if (store.Remove(nodeId.ToString("D")))
            Save(file, store);
    }

    private static Dictionary<string, string> Load(string path)
    {
        if (!File.Exists(path)) return new();
        try
        {
            var text = File.ReadAllText(path);
            if (string.IsNullOrWhiteSpace(text)) return new();
            var root = JsonNode.Parse(text) as JsonObject;
            var entries = root?["entries"] as JsonObject;
            if (entries is null) return new();
            var dict = new Dictionary<string, string>();
            foreach (var kv in entries)
            {
                if (kv.Value is JsonValue v && v.TryGetValue<string>(out var s))
                    dict[kv.Key] = s;
            }
            return dict;
        }
        catch
        {
            return new();
        }
    }

    private static void Save(string path, Dictionary<string, string> entries)
    {
        var dir = Path.GetDirectoryName(path);
        if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
        var entriesObj = new JsonObject();
        foreach (var kv in entries) entriesObj[kv.Key] = kv.Value;
        var root = new JsonObject { ["entries"] = entriesObj };
        var tmp = path + ".tmp";
        File.WriteAllText(tmp, root.ToJsonString(new JsonSerializerOptions { WriteIndented = true }));
        if (File.Exists(path)) File.Replace(tmp, path, null);
        else File.Move(tmp, path);
    }
}
