using System.Text.Json;
using System.Text.Json.Serialization;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>
/// The cloud settings of this PC, in <c>cloud.json</c>. The file is separate from
/// <c>settings.json</c>, so the settings export does not include it.
/// </summary>
public sealed class CloudPrefs
{
    public static readonly Uri DefaultEndpoint = new("https://api.towertail.com");

    private static readonly JsonSerializerOptions s_json = new() { WriteIndented = true };

    private sealed record Data
    {
        [JsonPropertyName("endpoint")] public string? Endpoint { get; init; }
        [JsonPropertyName("email")] public string? Email { get; init; }
        [JsonPropertyName("releasedHosts")] public List<Guid>? ReleasedHosts { get; init; }
        [JsonPropertyName("moveOffered")] public bool MoveOffered { get; init; }
        [JsonPropertyName("rulesMoveOffered")] public bool RulesMoveOffered { get; init; }
    }

    private readonly string _path;
    private readonly Logger? _log;
    private readonly object _gate = new();
    private Uri _endpoint = DefaultEndpoint;
    private string? _email;
    private IReadOnlySet<Guid> _released = new HashSet<Guid>();
    private bool _moveOffered;
    private bool _rulesMoveOffered;

    public CloudPrefs(string path, Logger? log = null)
    {
        _path = path;
        _log = log;
        Load();
    }

    public Uri Endpoint { get { lock (_gate) return _endpoint; } set { lock (_gate) _endpoint = value; } }
    public string? Email { get { lock (_gate) return _email; } set { lock (_gate) _email = value; } }
    public bool MoveOffered { get { lock (_gate) return _moveOffered; } set { lock (_gate) _moveOffered = value; } }

    /// <summary>Set after the rules move window opened by itself one time.</summary>
    public bool RulesMoveOffered { get { lock (_gate) return _rulesMoveOffered; } set { lock (_gate) _rulesMoveOffered = value; } }

    /// <summary>The ids of SSH hosts that a sign-out released. <see cref="Save"/> writes a change.</summary>
    public IReadOnlySet<Guid> ReleasedHosts { get { lock (_gate) return _released; } set { lock (_gate) _released = value; } }

    public void Save()
    {
        string json;
        lock (_gate)
        {
            json = JsonSerializer.Serialize(new Data
            {
                Endpoint = _endpoint.ToString(),
                Email = _email,
                ReleasedHosts = _released.OrderBy(g => g).ToList(),
                MoveOffered = _moveOffered,
                RulesMoveOffered = _rulesMoveOffered,
            }, s_json);
        }
        try
        {
            var dir = Path.GetDirectoryName(_path);
            if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
            var tmp = _path + ".tmp";
            File.WriteAllText(tmp, json);
            File.Move(tmp, _path, overwrite: true);
        }
        catch (Exception ex) { _log?.Warning($"cloud: cannot save {Path.GetFileName(_path)}: {ex.Message}"); }
    }

    private void Load()
    {
        if (!File.Exists(_path)) return;
        try
        {
            var data = JsonSerializer.Deserialize<Data>(File.ReadAllText(_path));
            if (data is null) return;
            if (Uri.TryCreate(data.Endpoint, UriKind.Absolute, out var endpoint)) _endpoint = endpoint;
            _email = data.Email;
            _released = (data.ReleasedHosts ?? []).ToHashSet();
            _moveOffered = data.MoveOffered;
            _rulesMoveOffered = data.RulesMoveOffered;
        }
        catch (Exception ex) when (ex is JsonException or IOException or UnauthorizedAccessException)
        {
            _log?.Warning($"cloud: bad {Path.GetFileName(_path)}, use the defaults: {ex.Message}");
        }
    }
}
