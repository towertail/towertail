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
    IReadOnlyList<string>? IgnoredProcs = null);

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
    Towertail.WinUI.State.LongProcRule? LongProc = null);

public sealed record RemoteThresholds(
    [property: JsonPropertyName("cpuWarn")] double CpuWarn,
    [property: JsonPropertyName("cpuCritical")] double CpuCritical,
    [property: JsonPropertyName("memWarn")] double MemWarn,
    [property: JsonPropertyName("memCritical")] double MemCritical,
    [property: JsonPropertyName("diskWarn")] double DiskWarn,
    [property: JsonPropertyName("diskCritical")] double DiskCritical);

public sealed record RemoteClientSettings(
    [property: JsonPropertyName("cardDensity")] string CardDensity);

/// <summary>One open alert of <c>GET /v1/alerts</c>. Other fields come with the alert tickets.</summary>
public sealed record RemoteAlert(
    [property: JsonPropertyName("node_id")] Guid NodeId,
    [property: JsonPropertyName("metric")] string Metric,
    [property: JsonPropertyName("tint")] string Tint);

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
