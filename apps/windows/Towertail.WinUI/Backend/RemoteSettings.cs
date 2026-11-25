using System.Text.Json.Serialization;

namespace Towertail.WinUI.Backend;

/// <summary>
/// Wire DTOs for the Towertail server REST / WS API. These mirror the
/// Go types in <c>server/internal/store/models.go</c> and
/// <c>server/internal/wire/wire.go</c>. The server uses <b>camelCase</b>
/// keys for node/settings/thresholds (matching Swift Codable defaults on
/// the Mac side) and <b>snake_case</b> for the sampler-wire types
/// (sample frames, enroll request/response) that mirror
/// <c>https://github.com/towertail/sampler/blob/main/docs/wire-format.md</c>. We match both conventions explicitly.
/// </summary>
public sealed record RemoteNode(
    [property: JsonPropertyName("id")] Guid Id,
    [property: JsonPropertyName("displayName")] string DisplayName,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("sshUser")] string? SshUser,
    [property: JsonPropertyName("sshHost")] string? SshHost,
    [property: JsonPropertyName("tags")] IReadOnlyList<string>? Tags,
    [property: JsonPropertyName("enabled")] bool Enabled,
    [property: JsonPropertyName("favorite")] bool Favorite,
    // Opt-in for disk scans through the server. Default off.
    [property: JsonPropertyName("allowDiskScan")] bool AllowDiskScan = false,
    // Null when the server is older than long-running process alerts. Null is not
    // sent, so the server keeps its stored values.
    [property: JsonPropertyName("customLongProc"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.LongProcRule? CustomLongProc = null,
    [property: JsonPropertyName("ignoredProcs"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<string>? IgnoredProcs = null,
    // The rules of the host. Null is not sent, so the server keeps its stored values.
    [property: JsonPropertyName("thresholdOverrides"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.ThresholdOverrides? ThresholdOverrides = null,
    [property: JsonPropertyName("mutedSignals"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.MutedSignals? MutedSignals = null);

public sealed record RemoteServerSettings(
    [property: JsonPropertyName("thresholds")] RemoteThresholds Thresholds,
    [property: JsonPropertyName("localPollingIntervalSeconds")] int LocalPollingIntervalSeconds,
    [property: JsonPropertyName("sshPollingIntervalSeconds")] int SshPollingIntervalSeconds,
    [property: JsonPropertyName("notificationsEnabled")] bool NotificationsEnabled,
    [property: JsonPropertyName("notifyWarn")] bool NotifyWarn,
    [property: JsonPropertyName("notifyCritical")] bool NotifyCritical,
    [property: JsonPropertyName("notifyDebounceSeconds")] int NotifyDebounceSeconds,
    [property: JsonPropertyName("autoUpdateSamplersEnabled")] bool AutoUpdateSamplersEnabled,
    [property: JsonPropertyName("postWakeGraceSeconds")] int PostWakeGraceSeconds,
    // Null when the server is older than long-running process alerts.
    [property: JsonPropertyName("longProc"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.LongProcRule? LongProc = null,
    // The org rules. Null is not sent, so the server keeps its stored values.
    [property: JsonPropertyName("alerts"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.AlertRules? Alerts = null,
    [property: JsonPropertyName("muted"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    Towertail.WinUI.State.MutedSignals? Muted = null);

public sealed record RemoteThresholds(
    [property: JsonPropertyName("cpuWarn")] double CpuWarn,
    [property: JsonPropertyName("cpuCritical")] double CpuCritical,
    [property: JsonPropertyName("memWarn")] double MemWarn,
    [property: JsonPropertyName("memCritical")] double MemCritical,
    [property: JsonPropertyName("diskWarn")] double DiskWarn,
    [property: JsonPropertyName("diskCritical")] double DiskCritical,
    // Health keys. Null is not sent, so the server keeps its stored values.
    [property: JsonPropertyName("procsWarn"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] int? ProcsWarn = null,
    [property: JsonPropertyName("procsCritical"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] int? ProcsCritical = null,
    [property: JsonPropertyName("zombiesWarn"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] int? ZombiesWarn = null,
    [property: JsonPropertyName("zombiesCritical"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] int? ZombiesCritical = null,
    [property: JsonPropertyName("health"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] Towertail.WinUI.State.HealthLimits? Health = null);

public sealed record RemoteClientSettings(
    [property: JsonPropertyName("cardDensity")] string CardDensity);

/// <summary>An alert of the server: one item of <c>GET /v1/alerts</c> and the body of <c>alert_updated</c>.</summary>
public sealed record RemoteAlert(
    [property: JsonPropertyName("id")] Guid Id,
    [property: JsonPropertyName("node_id")] Guid NodeId,
    [property: JsonPropertyName("metric")] string Metric,
    [property: JsonPropertyName("tint")] string Tint,
    [property: JsonPropertyName("acked_at")] DateTimeOffset? AckedAt = null,
    [property: JsonPropertyName("snoozed_until")] DateTimeOffset? SnoozedUntil = null)
{
    /// <summary>True while an ack or a snooze stops the notifications of the alert.</summary>
    public bool IsSilenced(DateTimeOffset now) => AckedAt is not null || SnoozedUntil > now;
}

public sealed record RemoteAlertList(
    [property: JsonPropertyName("alerts")] IReadOnlyList<RemoteAlert>? Alerts);

/// <summary>Body of <c>GET /v1/nodes/{id}/disk-scans</c>.</summary>
public sealed record DiskScanJobList(
    [property: JsonPropertyName("jobs")] IReadOnlyList<Towertail.WinUI.State.DiskScanJob>? Jobs);

/// <summary>Body of a 409 on <c>POST …/disk-scans</c>: the job that runs now.</summary>
public sealed record DiskScanConflict(
    [property: JsonPropertyName("job")] Towertail.WinUI.State.DiskScanJob? Job);

// Enroll request/response. Server uses snake_case for the sampler-enroll
// path (wire.go) so this stays snake.
public sealed record EnrollRequest(
    [property: JsonPropertyName("display_name")] string DisplayName,
    // The cloud node keeps the id of the SSH node. Left out when null.
    [property: JsonPropertyName("node_id"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] Guid? NodeId = null,
    [property: JsonPropertyName("machine_id"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? MachineId = null);

public sealed record EnrollResponse(
    [property: JsonPropertyName("node_id")] Guid NodeId,
    [property: JsonPropertyName("sampler_token")] string SamplerToken,
    [property: JsonPropertyName("endpoint")] string? Endpoint);
