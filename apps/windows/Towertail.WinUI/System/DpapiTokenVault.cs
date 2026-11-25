using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using Towertail.WinUI.Cloud;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Keeps the cloud user token in <c>cloud-token.json</c>, one entry for each endpoint key.
/// The ciphertext comes from DPAPI with the CurrentUser scope, so only this Windows user on
/// this PC can read it. The file layout is the one of <see cref="DpapiStore"/>.
/// </summary>
public sealed class DpapiTokenVault(string path) : ITokenVault
{
    public const string FileName = "cloud-token.json";

    private static readonly byte[] s_entropy = Encoding.UTF8.GetBytes("com.towertail.cloud");

    private readonly object _gate = new();

    public string? Get(string key)
    {
        lock (_gate)
        {
            if (!Load().TryGetValue(key, out var b64) || string.IsNullOrEmpty(b64)) return null;
            try
            {
                var plain = ProtectedData.Unprotect(Convert.FromBase64String(b64), s_entropy, DataProtectionScope.CurrentUser);
                return Encoding.UTF8.GetString(plain);
            }
            catch (Exception ex) when (ex is CryptographicException or FormatException)
            {
                // Written by another user or on another PC: the user signs in again.
                return null;
            }
        }
    }

    public void Set(string key, string token)
    {
        lock (_gate)
        {
            var entries = Load();
            var cipher = ProtectedData.Protect(Encoding.UTF8.GetBytes(token), s_entropy, DataProtectionScope.CurrentUser);
            entries[key] = Convert.ToBase64String(cipher);
            Save(entries);
        }
    }

    public void Delete(string key)
    {
        lock (_gate)
        {
            var entries = Load();
            if (entries.Remove(key)) Save(entries);
        }
    }

    private Dictionary<string, string> Load()
    {
        var dict = new Dictionary<string, string>();
        if (!File.Exists(path)) return dict;
        try
        {
            var text = File.ReadAllText(path);
            if (string.IsNullOrWhiteSpace(text)) return dict;
            if (JsonNode.Parse(text) is JsonObject { } root && root["entries"] is JsonObject entries)
                foreach (var kv in entries)
                    if (kv.Value is JsonValue v && v.TryGetValue<string>(out var s)) dict[kv.Key] = s;
        }
        catch (Exception ex) when (ex is JsonException or IOException) { }
        return dict;
    }

    private void Save(Dictionary<string, string> entries)
    {
        var dir = Path.GetDirectoryName(path);
        if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
        var obj = new JsonObject();
        foreach (var kv in entries) obj[kv.Key] = kv.Value;
        var root = new JsonObject { ["entries"] = obj };
        var tmp = path + ".tmp";
        File.WriteAllText(tmp, root.ToJsonString(new JsonSerializerOptions { WriteIndented = true }));
        File.Move(tmp, path, overwrite: true);
    }
}
