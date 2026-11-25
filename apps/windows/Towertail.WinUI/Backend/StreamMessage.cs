using System.Text.Json;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>
/// An event frame of <c>/v1/stream</c>. <c>Metric</c> and <c>Tint</c> are absent for some
/// kinds. A reachability change has the metric "reachability" and the tint "critical" or "ok".
/// <c>Notify</c> says that the server sends a notification, with <c>Title</c> and <c>Text</c>, for the alert <c>AlertId</c>.
/// </summary>
public sealed record StreamEvent(
    [property: JsonPropertyName("nodeId")] Guid NodeId,
    [property: JsonPropertyName("metric")] string? Metric,
    [property: JsonPropertyName("tint")] string? Tint,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("alertId")] Guid? AlertId = null,
    [property: JsonPropertyName("notify")] bool Notify = false,
    [property: JsonPropertyName("title")] string? Title = null,
    [property: JsonPropertyName("text")] string? Text = null);

/// <summary>One text frame of <c>/v1/stream</c> (docs/wire.md §4), decoded.</summary>
public abstract record StreamMessage
{
    public sealed record SampleFrame(Guid NodeId, Sample Sample) : StreamMessage;
    public sealed record NodeUpdatedFrame(RemoteNode Node) : StreamMessage;
    public sealed record SettingsUpdatedFrame(RemoteServerSettings Settings) : StreamMessage;
    public sealed record DiskScanUpdatedFrame(Guid NodeId, DiskScanJob Job) : StreamMessage;
    public sealed record EventFrame(StreamEvent Event) : StreamMessage;
    public sealed record AlertUpdatedFrame(RemoteAlert Alert) : StreamMessage;
    public sealed record HelloFrame(StreamHello Hello) : StreamMessage;
    public sealed record SummaryFrame(Guid NodeId, LiveSummary Summary) : StreamMessage;
    public sealed record DetailFrame(Guid NodeId, DetailTab Tab, LiveDetail Detail) : StreamMessage;
    public sealed record LeaseFrame(LeaseNotice Notice) : StreamMessage;
    public sealed record OtherFrame(string Type) : StreamMessage;

    /// <summary>Reads <c>type</c>, then the body of that case. Gives null for bad JSON and <see cref="OtherFrame"/> for an unknown type.</summary>
    public static StreamMessage? Decode(ReadOnlySpan<byte> utf8)
    {
        try
        {
            using var doc = JsonDocument.Parse(utf8.ToArray());
            var root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object) return null;
            var type = root.TryGetProperty("type", out var t) && t.ValueKind == JsonValueKind.String ? t.GetString() ?? "" : "";
            try { return Body(type, root) ?? new OtherFrame(type); }
            catch (Exception ex) when (ex is JsonException or InvalidOperationException or FormatException)
            {
                return new OtherFrame(type);
            }
        }
        catch (JsonException) { return null; }
    }

    private static StreamMessage? Body(string type, JsonElement root)
    {
        var o = SampleCodec.Options;
        switch (type)
        {
            case "sample":
                if (ReadNodeId(root) is not Guid sid || root.GetProperty("sample").Deserialize<Sample>(o) is not { V: 1 } sample) return null;
                return new SampleFrame(sid, sample);
            case "node_updated":
                return root.GetProperty("node").Deserialize<RemoteNode>(o) is { } node ? new NodeUpdatedFrame(node) : null;
            case "settings_updated":
                return root.GetProperty("settings").Deserialize<RemoteServerSettings>(o) is { } s ? new SettingsUpdatedFrame(s) : null;
            case "disk_scan_updated":
                return root.GetProperty("job").Deserialize<DiskScanJob>(o) is { } job
                    ? new DiskScanUpdatedFrame(ReadNodeId(root) ?? job.NodeId, job) : null;
            case "event":
                return root.GetProperty("event").Deserialize<StreamEvent>(o) is { } e ? new EventFrame(e) : null;
            case "alert_updated":
                return root.GetProperty("alert").Deserialize<RemoteAlert>(o) is { } a ? new AlertUpdatedFrame(a) : null;
            case "hello":
                return root.GetProperty("hello").Deserialize<StreamHello>(o) is { } h ? new HelloFrame(h) : null;
            case "summary":
                return ReadNodeId(root) is Guid id && root.GetProperty("summary").Deserialize<LiveSummary>(o) is { } sum
                    ? new SummaryFrame(id, sum) : null;
            case "detail":
                return ReadNodeId(root) is Guid did
                    && root.GetProperty("tab").Deserialize<DetailTab>(o) is var tab
                    && root.GetProperty("detail").Deserialize<LiveDetail>(o) is { } d
                    ? new DetailFrame(did, tab, d) : null;
            case "lease":
                return root.GetProperty("lease").Deserialize<LeaseNotice>(o) is { } l ? new LeaseFrame(l) : null;
            default:
                return null;
        }
    }

    private static Guid? ReadNodeId(JsonElement root)
        => root.TryGetProperty("nodeId", out var n) && n.ValueKind == JsonValueKind.String && n.TryGetGuid(out var g) ? g : null;
}
