using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

[JsonConverter(typeof(JsonStringEnumConverter<NodeKind>))]
public enum NodeKind
{
    [JsonStringEnumMemberName("local")] Local,
    [JsonStringEnumMemberName("ssh")] Ssh,
}

/// <summary>
/// How we authenticate to an SSH host. "Key" covers both on-disk private keys
/// and Pageant/agent; "Password" pulls the plaintext from DPAPI at connect time.
/// Values on the wire match the Mac side (<c>"key"</c> / <c>"password"</c>).
/// </summary>
[JsonConverter(typeof(JsonStringEnumConverter<AuthMethod>))]
public enum AuthMethod
{
    [JsonStringEnumMemberName("key")] Key,
    [JsonStringEnumMemberName("password")] Password,
}

/// <summary>
/// A monitored server. Identity is a UUID so node IDs round-trip cleanly between Mac and Windows.
/// JSON encoding is compatible with Mac's Node.swift — camelCase keys, platform-neutral UUIDs.
/// </summary>
public sealed record Node
{
    public Guid Id { get; init; } = Guid.NewGuid();
    public string DisplayName { get; init; } = "";
    public NodeKind Kind { get; init; } = NodeKind.Local;
    public string? SshUser { get; init; }
    public string? SshHost { get; init; }
    /// <summary>SSH port; null → default 22. Optional so existing records round-trip unchanged.</summary>
    public int? SshPort { get; init; }
    /// <summary>Defaults to Key for back-compat — records missing the field on disk keep key-only behavior.</summary>
    public AuthMethod AuthMethod { get; init; } = AuthMethod.Key;
    /// <summary>
    /// SHA256-base64 fingerprint of the remote host key we trust for this node.
    /// null → no key pinned yet; first connect prompts the user. Mismatch refuses.
    /// </summary>
    public string? KnownHostFingerprint { get; init; }
    public IReadOnlyList<string> Tags { get; init; } = Array.Empty<string>();
    public bool Enabled { get; init; } = true;
    public bool IconOnWarn { get; init; } = true;
    public bool IconOnCritical { get; init; } = true;
    public bool NotifyOnWarn { get; init; } = true;
    public bool NotifyOnCritical { get; init; } = true;
    public MetricThresholds? CustomThresholds { get; init; }
    public DateTime? SnoozedUntil { get; init; }
    public bool Favorite { get; init; }

    [JsonIgnore]
    public bool IsSnoozed => SnoozedUntil is { } u && u > DateTime.UtcNow;

    /// <summary>Effective port (SshPort ?? 22). Used by the SSH factory and UI.</summary>
    [JsonIgnore]
    public int EffectiveSshPort => SshPort ?? 22;

    [JsonIgnore]
    public string UserAtHost => Kind switch
    {
        NodeKind.Local => "local",
        NodeKind.Ssh => $"{(string.IsNullOrEmpty(SshUser) ? "?" : SshUser)}@{(string.IsNullOrEmpty(SshHost) ? "?" : SshHost)}",
        _ => "?",
    };

    public static Node LocalWindows(string displayName = "This PC")
        => new() { DisplayName = displayName, Kind = NodeKind.Local };
}

public sealed record MetricThresholds(
    double CpuWarn,
    double CpuCritical,
    double MemWarn,
    double MemCritical,
    double DiskWarn,
    double DiskCritical,
    // Minimum consecutive over-threshold samples required before a metric
    // can escalate. 1 = original "fire on first crossing"; per-metric so a
    // bursty CPU doesn't force users to slow disk alerts down too. Shared
    // between warn and critical; clear path is unconditional.
    int CpuSustainSamples = 1,
    int MemSustainSamples = 1,
    int DiskSustainSamples = 1,
    // Host health: absolute counts, not fractions.
    int ProcsWarn = MetricThresholds.DefaultProcsWarn,
    int ProcsCritical = MetricThresholds.DefaultProcsCritical,
    int ZombiesWarn = MetricThresholds.DefaultZombiesWarn,
    int ZombiesCritical = MetricThresholds.DefaultZombiesCritical)
{
    public const int DefaultProcsWarn = 5000;
    public const int DefaultProcsCritical = 20000;
    public const int DefaultZombiesWarn = 200;
    public const int DefaultZombiesCritical = 2000;

    /// <summary>Copy with health counts clamped to min 1 and warn &lt;= critical.</summary>
    public MetricThresholds WithHealth(int procsWarn, int procsCritical, int zombiesWarn, int zombiesCritical)
    {
        procsWarn = Math.Max(1, procsWarn);
        zombiesWarn = Math.Max(1, zombiesWarn);
        return this with
        {
            ProcsWarn = procsWarn,
            ProcsCritical = Math.Max(procsWarn, procsCritical),
            ZombiesWarn = zombiesWarn,
            ZombiesCritical = Math.Max(zombiesWarn, zombiesCritical),
        };
    }

    /// <summary>
    /// Thresholds that apply to one host. The per-node editor has no health fields, so the
    /// health counts always come from <paramref name="global"/>.
    /// </summary>
    public static MetricThresholds Effective(MetricThresholds global, MetricThresholds? custom)
        => custom is null ? global : custom.WithHealth(global.ProcsWarn, global.ProcsCritical, global.ZombiesWarn, global.ZombiesCritical);

    public static MetricThresholds Defaults => new(
        CpuWarn: 0.75, CpuCritical: 0.90,
        MemWarn: 0.75, MemCritical: 0.90,
        DiskWarn: 0.85, DiskCritical: 0.95);
}
