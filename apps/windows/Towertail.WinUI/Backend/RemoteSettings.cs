using System.Text.Json.Serialization;

namespace Towertail.WinUI.Backend;

/// <summary>
/// Wire DTOs for the Towertail server REST / WS API. These mirror the
/// Go types in <c>server/internal/store/models.go</c> and
/// <c>server/internal/wire/wire.go</c>. The server uses <b>camelCase</b>
/// keys for node/settings/thresholds (matching Swift Codable defaults on
/// the Mac side) and <b>snake_case</b> for the sampler-wire types
/// (sample frames, enroll request/response) that mirror
/// <c>docs/sampler.md</c>. We match both conventions explicitly.
/// </summary>
public sealed record RemoteNode(
    [property: JsonPropertyName("id")] Guid Id,
    [property: JsonPropertyName("displayName")] string DisplayName,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("sshUser")] string? SshUser,
    [property: JsonPropertyName("sshHost")] string? SshHost,
    [property: JsonPropertyName("tags")] IReadOnlyList<string>? Tags,
    [property: JsonPropertyName("enabled")] bool Enabled,
    [property: JsonPropertyName("favorite")] bool Favorite);

public sealed record RemoteServerSettings(
    [property: JsonPropertyName("thresholds")] RemoteThresholds Thresholds,
    [property: JsonPropertyName("localPollingIntervalSeconds")] int LocalPollingIntervalSeconds,
    [property: JsonPropertyName("sshPollingIntervalSeconds")] int SshPollingIntervalSeconds,
    [property: JsonPropertyName("notificationsEnabled")] bool NotificationsEnabled,
    [property: JsonPropertyName("notifyWarn")] bool NotifyWarn,
    [property: JsonPropertyName("notifyCritical")] bool NotifyCritical,
    [property: JsonPropertyName("notifyDebounceSeconds")] int NotifyDebounceSeconds,
    [property: JsonPropertyName("autoUpdateSamplersEnabled")] bool AutoUpdateSamplersEnabled,
    [property: JsonPropertyName("postWakeGraceSeconds")] int PostWakeGraceSeconds);

public sealed record RemoteThresholds(
    [property: JsonPropertyName("cpuWarn")] double CpuWarn,
    [property: JsonPropertyName("cpuCritical")] double CpuCritical,
    [property: JsonPropertyName("memWarn")] double MemWarn,
    [property: JsonPropertyName("memCritical")] double MemCritical,
    [property: JsonPropertyName("diskWarn")] double DiskWarn,
    [property: JsonPropertyName("diskCritical")] double DiskCritical);

public sealed record RemoteClientSettings(
    [property: JsonPropertyName("cardDensity")] string CardDensity);

// Stream frames: envelope uses camelCase (`nodeId`) per
// server/internal/wire/wire.go WSMessage. The inner sample payload is
// the raw sampler NDJSON which stays snake_case per docs/sampler.md §4.
public sealed record RemoteStreamFrame(
    [property: JsonPropertyName("type")] string Type,
    [property: JsonPropertyName("nodeId")] Guid? NodeId,
    [property: JsonPropertyName("sample")] Towertail.WinUI.State.Sample? Sample);

// Enroll request/response. Server uses snake_case for the sampler-enroll
// path (wire.go) so this stays snake.
public sealed record EnrollRequest(
    [property: JsonPropertyName("display_name")] string DisplayName);

public sealed record EnrollResponse(
    [property: JsonPropertyName("node_id")] Guid NodeId,
    [property: JsonPropertyName("sampler_token")] string SamplerToken,
    [property: JsonPropertyName("endpoint")] string? Endpoint);
