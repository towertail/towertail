using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

// Lease-mode frames of `/v1/stream` (docs/wire.md §4).

/// <summary>The part of a sample that a <c>detail</c> frame carries.</summary>
[JsonConverter(typeof(JsonStringEnumConverter<DetailTab>))]
public enum DetailTab
{
    [JsonStringEnumMemberName("processes")] Processes,
    [JsonStringEnumMemberName("ports")] Ports,
    [JsonStringEnumMemberName("disks")] Disks,
    [JsonStringEnumMemberName("health")] Health,
}

/// <summary>Slow facts of a host. The server sends them in the first summary of a lease and after a change.</summary>
public sealed record HostFacts(
    [property: JsonPropertyName("os")] string? Os = null,
    [property: JsonPropertyName("arch")] string? Arch = null,
    [property: JsonPropertyName("kernel")] string? Kernel = null,
    [property: JsonPropertyName("cores")] int? Cores = null,
    [property: JsonPropertyName("mem_total")] long? MemTotal = null,
    [property: JsonPropertyName("uptime_s")] long? UptimeS = null,
    [property: JsonPropertyName("sampler")] string? Sampler = null);

/// <summary><see cref="HealthInfo"/> without the zombie parents, plus the worst inode ratio.</summary>
[JsonConverter(typeof(HealthBriefConverter))]
public sealed record HealthBrief(HealthInfo Info, double? Inodes = null);

/// <summary>Reads <see cref="HealthInfo"/> and the extra key <c>inodes</c> from one object.</summary>
public sealed class HealthBriefConverter : JsonConverter<HealthBrief>
{
    public override HealthBrief? Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        using var doc = JsonDocument.ParseValue(ref reader);
        var root = doc.RootElement;
        var info = root.Deserialize<HealthInfo>(options) ?? throw new JsonException("empty health");
        double? inodes = root.TryGetProperty("inodes", out var i) && i.ValueKind == JsonValueKind.Number
            ? i.GetDouble() : null;
        return new HealthBrief(info, inodes);
    }

    public override void Write(Utf8JsonWriter writer, HealthBrief value, JsonSerializerOptions options)
    {
        var node = JsonSerializer.SerializeToNode(value.Info, options) as JsonObject ?? new JsonObject();
        if (value.Inodes is double d) node["inodes"] = d;
        node.WriteTo(writer);
    }
}

/// <summary>
/// The short sample of a leased host. <c>Cpu</c> is a percent. <c>Mem</c> and <c>Disk</c> are
/// ratios. <c>Rx</c> and <c>Tx</c> are bytes each second. <c>Load</c> is <c>cpu.load_1</c>.
/// </summary>
public sealed record LiveSummary(
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("cpu")] double Cpu,
    [property: JsonPropertyName("mem")] double Mem,
    [property: JsonPropertyName("disk")] double Disk,
    [property: JsonPropertyName("rx")] long Rx,
    [property: JsonPropertyName("tx")] long Tx,
    [property: JsonPropertyName("load")] double Load,
    [property: JsonPropertyName("procs")] int? Procs = null,
    [property: JsonPropertyName("health")] HealthBrief? Health = null,
    [property: JsonPropertyName("host")] HostFacts? Host = null);

/// <summary>The parts of a sample for the open tab.</summary>
public sealed record LiveDetail(
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("procs")] ProcList? Procs = null,
    [property: JsonPropertyName("ports")] PortList? Ports = null,
    [property: JsonPropertyName("disks")] IReadOnlyList<DiskSample>? Disks = null,
    [property: JsonPropertyName("disk_io")] DiskIoInfo? DiskIo = null,
    [property: JsonPropertyName("health")] HealthInfo? Health = null);

public sealed record ViewDetail(Guid Host, DetailTab Tab);

/// <summary>The <c>view</c> frame that the client sends. <see cref="Empty"/> ends the lease.</summary>
public sealed record LiveView(IReadOnlyList<Guid> Hosts, ViewDetail? Detail = null, int Ttl = 60)
{
    public static LiveView Empty { get; } = new(Array.Empty<Guid>());

    public bool Equals(LiveView? other)
        => other is not null && Ttl == other.Ttl && Detail == other.Detail && Hosts.SequenceEqual(other.Hosts);

    public override int GetHashCode()
    {
        var h = new HashCode();
        foreach (var host in Hosts) h.Add(host);
        h.Add(Detail);
        h.Add(Ttl);
        return h.ToHashCode();
    }

    /// <summary>The <c>view</c> frame of wire §4. <c>detail</c> is written only when set.</summary>
    public string ToFrameJson()
    {
        using var ms = new MemoryStream();
        using (var w = new Utf8JsonWriter(ms))
        {
            w.WriteStartObject();
            w.WriteString("type", "view");
            w.WriteStartArray("hosts");
            foreach (var h in Hosts) w.WriteStringValue(h);
            w.WriteEndArray();
            if (Detail is { } d)
            {
                w.WriteStartObject("detail");
                w.WriteString("host", d.Host);
                w.WriteString("tab", JsonSerializer.SerializeToNode(d.Tab)!.GetValue<string>());
                w.WriteEndObject();
            }
            w.WriteNumber("ttl", Ttl);
            w.WriteEndObject();
        }
        return System.Text.Encoding.UTF8.GetString(ms.ToArray());
    }
}

/// <summary>A <c>lease</c> frame. <c>Reason</c> is "limit" when the server ends the lease because of its limit for each user.</summary>
public sealed record LeaseNotice(
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("reason")] string? Reason = null)
{
    [JsonIgnore]
    public bool IsLimit => State == "ended" && Reason == "limit";
}

/// <summary>The <c>hello</c> frame, the first frame of a lease connection.</summary>
public sealed record StreamHello(
    [property: JsonPropertyName("lease")] bool Lease,
    [property: JsonPropertyName("renew_s")] int RenewS,
    [property: JsonPropertyName("max_ttl_s")] int MaxTtlS);

/// <summary>Maps the full view tags to the detail tab that the cloud sends.</summary>
public static class DetailTabMap
{
    /// <summary>
    /// The tab for a full view tag, or null when the view needs no detail (the Usage view
    /// of disk: disk scans use their own route). <paramref name="portsSubTabShown"/> is true
    /// when the Ports sub-tab of the net tab shows.
    /// </summary>
    public static DetailTab? For(string fullViewTag, bool portsSubTabShown, bool diskUsageShown) => fullViewTag switch
    {
        "cpu" or "mem" or "procs" => DetailTab.Processes,
        "net" => portsSubTabShown ? DetailTab.Ports : DetailTab.Processes,
        "disk" => diskUsageShown ? null : DetailTab.Disks,
        "health" => DetailTab.Health,
        _ => null,
    };
}
