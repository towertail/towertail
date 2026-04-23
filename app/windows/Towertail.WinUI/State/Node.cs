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
    double DiskCritical)
{
    public static MetricThresholds Defaults => new(
        CpuWarn: 0.75, CpuCritical: 0.90,
        MemWarn: 0.75, MemCritical: 0.90,
        DiskWarn: 0.85, DiskCritical: 0.95);
}
