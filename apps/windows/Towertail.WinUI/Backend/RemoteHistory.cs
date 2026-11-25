using System.Text.Json.Serialization;

namespace Towertail.WinUI.Backend;

// Wire types of the history routes (docs/wire.md): history, processes, disks.

/// <summary>One point of a history series. <see cref="Max"/> is absent when it is 0.</summary>
public sealed record RemoteHistoryPoint(
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("value")] double Value,
    [property: JsonPropertyName("max")] double? Max = null);

/// <summary><c>NodeHistoryMultiResponse</c>: one series for each metric name.</summary>
public sealed record RemoteHistoryResponse
{
    [JsonPropertyName("node_id")] public Guid NodeId { get; init; }
    [JsonPropertyName("series")] public Dictionary<string, List<RemoteHistoryPoint>> Series { get; init; } = new();
    [JsonPropertyName("from")] public DateTime From { get; init; }
    [JsonPropertyName("to")] public DateTime To { get; init; }
    [JsonPropertyName("resolution")] public string Resolution { get; init; } = "";
    [JsonPropertyName("clamped")] public bool? Clamped { get; init; }
}

/// <summary><c>NodeDisksResponse</c>: the used space of each mount over time.</summary>
public sealed record RemoteDiskHistory
{
    [JsonPropertyName("mounts")] public List<RemoteDiskMount> Mounts { get; init; } = new();
}

public sealed record RemoteDiskMount
{
    [JsonPropertyName("mount")] public string Mount { get; init; } = "";
    [JsonPropertyName("fs")] public string Fs { get; init; } = "";
    [JsonPropertyName("points")] public List<RemoteDiskPoint> Points { get; init; } = new();
}

public sealed record RemoteDiskPoint(
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("used")] ulong Used,
    [property: JsonPropertyName("total")] ulong Total,
    [property: JsonPropertyName("inodes_used")] ulong? InodesUsed = null,
    [property: JsonPropertyName("inodes_total")] ulong? InodesTotal = null)
{
    public double Ratio => Total > 0 ? (double)Used / Total : 0;

    /// <summary>The share of used inodes, or null when the host sends none.</summary>
    public double? InodeRatio => InodesUsed is { } u && InodesTotal is { } t && t > 0 ? (double)u / t : null;
}

/// <summary><c>ProcessSnapshot</c>. Raw rows have a PID. Rollup rows have a peak.</summary>
public sealed record RemoteProcessSnapshot
{
    [JsonPropertyName("node_id")] public Guid NodeId { get; init; }
    [JsonPropertyName("ts")] public DateTime Ts { get; init; }
    [JsonPropertyName("tier")] public string Tier { get; init; } = "raw";
    [JsonPropertyName("bucket_s")] public int BucketS { get; init; }
    [JsonPropertyName("by")] public string By { get; init; } = "cpu";
    [JsonPropertyName("total")] public int? Total { get; init; }
    [JsonPropertyName("prev")] public DateTime? Prev { get; init; }
    [JsonPropertyName("next")] public DateTime? Next { get; init; }
    [JsonPropertyName("clamped")] public bool? Clamped { get; init; }
    [JsonPropertyName("processes")] public List<RemoteSnapshotProcess> Processes { get; init; } = new();

    /// <summary>The size of the response body. The client sets it after the decode.</summary>
    [JsonIgnore] public int Bytes { get; init; }
}

/// <summary><c>SnapshotProcess</c>. <c>pid</c> is raw only. <c>cpu_pct_max</c> is rollup only.</summary>
public sealed record RemoteSnapshotProcess
{
    [JsonPropertyName("key")] public string Key { get; init; } = "";
    [JsonPropertyName("pid")] public int? Pid { get; init; }
    [JsonPropertyName("ppid")] public int? Ppid { get; init; }
    [JsonPropertyName("parent")] public string? Parent { get; init; }
    [JsonPropertyName("user")] public string? User { get; init; }
    [JsonPropertyName("name")] public string Name { get; init; } = "";
    [JsonPropertyName("cmd")] public string Cmd { get; init; } = "";
    [JsonPropertyName("repo")] public string? Repo { get; init; }
    [JsonPropertyName("cwd")] public string? Cwd { get; init; }
    [JsonPropertyName("cpu_pct")] public double CpuPct { get; init; }
    [JsonPropertyName("cpu_pct_max")] public double? CpuPctMax { get; init; }
    [JsonPropertyName("rss")] public ulong Rss { get; init; }
    [JsonPropertyName("io_bps")] public double? IoBps { get; init; }
    [JsonPropertyName("threads")] public int? Threads { get; init; }
    [JsonPropertyName("state")] public string? State { get; init; }
    [JsonPropertyName("start_ts")] public DateTime? StartTs { get; init; }
}

/// <summary><c>NodeProcessesResponse</c>. Point i of a series is at <c>series_from + i * series_step_s</c>.</summary>
public sealed record RemoteOffenders
{
    [JsonPropertyName("node_id")] public Guid NodeId { get; init; }
    [JsonPropertyName("from")] public DateTime From { get; init; }
    [JsonPropertyName("to")] public DateTime To { get; init; }
    [JsonPropertyName("clamped")] public bool? Clamped { get; init; }
    [JsonPropertyName("series_from")] public DateTime? SeriesFrom { get; init; }
    [JsonPropertyName("series_step_s")] public int? SeriesStepS { get; init; }
    [JsonPropertyName("processes")] public List<RemoteOffender> Processes { get; init; } = new();
}

/// <summary><c>TopProcess</c>.</summary>
public sealed record RemoteOffender
{
    [JsonPropertyName("key")] public string Key { get; init; } = "";
    [JsonPropertyName("name")] public string Name { get; init; } = "";
    [JsonPropertyName("cmd")] public string Cmd { get; init; } = "";
    [JsonPropertyName("repo")] public string? Repo { get; init; }
    [JsonPropertyName("cwd")] public string? Cwd { get; init; }
    [JsonPropertyName("parent")] public string? Parent { get; init; }
    [JsonPropertyName("cpu_pct_avg")] public double CpuPctAvg { get; init; }
    [JsonPropertyName("cpu_pct_max")] public double CpuPctMax { get; init; }
    [JsonPropertyName("rss_max")] public ulong RssMax { get; init; }
    [JsonPropertyName("io_bps_avg")] public double? IoBpsAvg { get; init; }
    [JsonPropertyName("series")] public RemoteProcSeries? Series { get; init; }
}

/// <summary><c>ProcSeries</c>. <c>io</c> is absent when the host has no I/O data.</summary>
public sealed record RemoteProcSeries
{
    [JsonPropertyName("cpu")] public List<double> Cpu { get; init; } = new();
    [JsonPropertyName("rss")] public List<ulong> Rss { get; init; } = new();
    [JsonPropertyName("io")] public List<double>? Io { get; init; }
}

/// <summary><c>ProcessDetail</c>. <see cref="Facts"/> is null when the window ends more than 1 h ago.</summary>
public sealed record RemoteProcessDetail
{
    [JsonPropertyName("node_id")] public Guid NodeId { get; init; }
    [JsonPropertyName("key")] public string Key { get; init; } = "";
    [JsonPropertyName("name")] public string Name { get; init; } = "";
    [JsonPropertyName("cmd")] public string Cmd { get; init; } = "";
    [JsonPropertyName("repo")] public string? Repo { get; init; }
    [JsonPropertyName("from")] public DateTime From { get; init; }
    [JsonPropertyName("to")] public DateTime To { get; init; }
    [JsonPropertyName("resolution")] public string Resolution { get; init; } = "";
    [JsonPropertyName("clamped")] public bool? Clamped { get; init; }
    [JsonPropertyName("points")] public List<RemoteProcPoint> Points { get; init; } = new();
    [JsonPropertyName("facts")] public RemoteProcessFacts? Facts { get; init; }
}

public sealed record RemoteProcPoint(
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("cpu_pct")] double CpuPct,
    [property: JsonPropertyName("rss")] ulong Rss,
    [property: JsonPropertyName("io_bps")] double? IoBps = null);

/// <summary><c>ProcessFacts</c>.</summary>
public sealed record RemoteProcessFacts
{
    [JsonPropertyName("seen_at")] public DateTime SeenAt { get; init; }
    [JsonPropertyName("pid")] public int Pid { get; init; }
    [JsonPropertyName("ppid")] public int? Ppid { get; init; }
    [JsonPropertyName("parent")] public string? Parent { get; init; }
    [JsonPropertyName("user")] public string? User { get; init; }
    [JsonPropertyName("cwd")] public string? Cwd { get; init; }
    [JsonPropertyName("state")] public string? State { get; init; }
    [JsonPropertyName("threads")] public int? Threads { get; init; }
    [JsonPropertyName("start_ts")] public DateTime? StartTs { get; init; }
    [JsonPropertyName("listen_tcp")] public List<int>? ListenTcp { get; init; }
    [JsonPropertyName("listen_udp")] public List<int>? ListenUdp { get; init; }
}

public static class CloudHistoryError
{
    /// <summary>The server text of an error, else the text of the exception.</summary>
    public static string Message(Exception ex)
        => ex is RemoteException { Body.Length: > 0 } r ? RemoteException.ErrorText(r.Body) : ex.Message;
}
