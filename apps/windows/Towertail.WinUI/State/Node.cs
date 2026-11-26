using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

[JsonConverter(typeof(JsonStringEnumConverter<NodeKind>))]
public enum NodeKind
{
    [JsonStringEnumMemberName("local")] Local,
    [JsonStringEnumMemberName("ssh")] Ssh,
    [JsonStringEnumMemberName("cloud")] Cloud,
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
/// The link of a host to the cloud. Same JSON keys as the Mac <c>CloudLink</c>:
/// <c>reboot</c> is the boot mode, <c>lastStatus</c> the sampler status object.
/// </summary>
public sealed record CloudLink(
    [property: JsonPropertyName("enrolledAt")] DateTime EnrolledAt,
    [property: JsonPropertyName("reboot")] string? Reboot = null,
    [property: JsonPropertyName("samplerVersion")] string? SamplerVersion = null,
    [property: JsonPropertyName("lastStatus")] JsonElement? LastStatus = null);

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
    /// <summary>Per-metric threshold overrides; null uses the global thresholds.</summary>
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public ThresholdOverrides? ThresholdOverrides { get => _overrides; init => _overrides = value; }
    private readonly ThresholdOverrides? _overrides;

    /// <summary>Old all-or-nothing key. Read only: a value fills <see cref="ThresholdOverrides"/> when it has none.</summary>
    [JsonPropertyName("customThresholds")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public MetricThresholds? LegacyCustomThresholds
    {
        get => null;
        init { if (value is not null) _overrides ??= ThresholdOverrides.FromLegacy(value); }
    }
    /// <summary>Per-node alert rules; null inherits the global rules.</summary>
    public AlertRules? CustomAlerts { get; init; }
    public DateTime? SnoozedUntil { get; init; }
    public bool Favorite { get; init; }
    /// <summary>Per-node long-running process rule; null inherits the global rule.</summary>
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public LongProcRule? CustomLongProc { get; init; }
    /// <summary>Process names to ignore on this host, added to the rule's list.</summary>
    public IReadOnlyList<string> IgnoredProcs { get; init; } = Array.Empty<string>();
    /// <summary>Signals muted on this host, added to the global mutes.</summary>
    public MutedSignals MutedSignals { get; init; } = MutedSignals.None;

    /// <summary>Set when the host reports to the cloud: a cloud-only host or an SSH host with a sampler in the cloud.</summary>
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public CloudLink? Cloud { get; init; }

    public AlertRules EffectiveAlerts(AlertRules global) => CustomAlerts ?? global;

    [JsonIgnore]
    public bool IsCloud => Cloud != null || Kind == NodeKind.Cloud;

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
        NodeKind.Cloud => DisplayName,
        _ => "?",
    };

    /// <summary>The ssh command line for a terminal. Adds <c>-p</c> only when the node has a port.</summary>
    [JsonIgnore]
    public string SshCommand => SshPort is int p ? $"ssh -p {p} {UserAtHost}" : $"ssh {UserAtHost}";

    public static Node LocalWindows(string displayName = "This PC")
        => new() { DisplayName = displayName, Kind = NodeKind.Local };

    public static Node CloudOnly(Guid id, string displayName)
        => new() { Id = id, DisplayName = displayName, Kind = NodeKind.Cloud };
}

[JsonConverter(typeof(MetricThresholdsJsonConverter))]
public sealed record MetricThresholds(
    double CpuWarn,
    double CpuCritical,
    double MemWarn,
    double MemCritical,
    double DiskWarn,
    double DiskCritical,
    // Host health: absolute counts, not fractions.
    int ProcsWarn = MetricThresholds.DefaultProcsWarn,
    int ProcsCritical = MetricThresholds.DefaultProcsCritical,
    int ZombiesWarn = MetricThresholds.DefaultZombiesWarn,
    int ZombiesCritical = MetricThresholds.DefaultZombiesCritical,
    HealthLimits? Health = null)
{
    public const int DefaultProcsWarn = 5000;
    public const int DefaultProcsCritical = 20000;
    public const int DefaultZombiesWarn = 200;
    public const int DefaultZombiesCritical = 2000;

    public HealthLimits HealthOrDefault => Health ?? HealthLimits.Defaults;

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

    /// <summary>Warn/critical pair for one metric. Counts are rounded.</summary>
    public ThresholdPair Get(ThresholdMetric m) => m switch
    {
        ThresholdMetric.Cpu => new(CpuWarn, CpuCritical),
        ThresholdMetric.Mem => new(MemWarn, MemCritical),
        ThresholdMetric.Disk => new(DiskWarn, DiskCritical),
        ThresholdMetric.Procs => new(ProcsWarn, ProcsCritical),
        ThresholdMetric.Zombies => new(ZombiesWarn, ZombiesCritical),
        ThresholdMetric.Pids => HealthOrDefault.Pids,
        ThresholdMetric.Files => HealthOrDefault.Files,
        ThresholdMetric.Inodes => HealthOrDefault.Inodes,
        ThresholdMetric.MemPressure => HealthOrDefault.MemPressure,
        _ => HealthOrDefault.IoPressure,
    };

    /// <summary>Copy with the pair of one metric replaced. Critical stays at or above warn.</summary>
    public MetricThresholds With(ThresholdMetric m, ThresholdPair pair)
    {
        pair = pair.Clamped();
        var h = HealthOrDefault;
        return m switch
        {
            ThresholdMetric.Cpu => this with { CpuWarn = pair.Warn, CpuCritical = pair.Critical },
            ThresholdMetric.Mem => this with { MemWarn = pair.Warn, MemCritical = pair.Critical },
            ThresholdMetric.Disk => this with { DiskWarn = pair.Warn, DiskCritical = pair.Critical },
            ThresholdMetric.Procs => WithHealth((int)Math.Round(pair.Warn), (int)Math.Round(pair.Critical), ZombiesWarn, ZombiesCritical),
            ThresholdMetric.Zombies => WithHealth(ProcsWarn, ProcsCritical, (int)Math.Round(pair.Warn), (int)Math.Round(pair.Critical)),
            ThresholdMetric.Pids => this with { Health = h with { Pids = pair } },
            ThresholdMetric.Files => this with { Health = h with { Files = pair } },
            ThresholdMetric.Inodes => this with { Health = h with { Inodes = pair } },
            ThresholdMetric.MemPressure => this with { Health = h with { MemPressure = pair } },
            _ => this with { Health = h with { IoPressure = pair } },
        };
    }

    /// <summary>Thresholds that apply to one host: each overridden metric replaces the global pair.</summary>
    public static MetricThresholds Effective(MetricThresholds global, ThresholdOverrides? overrides)
        => overrides?.Applied(global) ?? global;

    public static MetricThresholds Defaults => new(
        CpuWarn: 0.75, CpuCritical: 0.90,
        MemWarn: 0.75, MemCritical: 0.90,
        DiskWarn: 0.85, DiskCritical: 0.95);
}
