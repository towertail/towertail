using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Read-model for <c>dist/samplers/manifest.json</c> — the sha256-per-triple table the app
/// uses to decide whether a remote binary is stale.
/// </summary>
public sealed record SamplerManifest(
    [property: JsonPropertyName("version")] string Version,
    [property: JsonPropertyName("sha")] string Sha,
    [property: JsonPropertyName("binaries")] IReadOnlyDictionary<string, string> Binaries)
{
    public static SamplerManifest? LoadFromBundle(string? bundleRoot = null)
    {
        var root = bundleRoot ?? AppContext.BaseDirectory;
        var path = Path.Combine(root, "Assets", "samplers", "manifest.json");
        if (!File.Exists(path)) return null;
        try
        {
            var text = File.ReadAllText(path);
            return JsonSerializer.Deserialize<SamplerManifest>(text);
        }
        catch
        {
            return null;
        }
    }

    public string? ExpectedSha(string triple)
        => Binaries.TryGetValue(triple, out var sha) ? sha : null;
}
