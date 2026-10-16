using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

/// <summary>
/// What to scan. Null fields use the sampler defaults (wire-format.md §3.4).
/// The JSON keys are the server <c>DiskScanRequest</c> (docs/wire.md §3.1).
/// </summary>
public sealed record DiskScanRequest(
    [property: JsonPropertyName("root")] string? Root = null,
    [property: JsonPropertyName("max_depth")] int? MaxDepth = null,
    [property: JsonPropertyName("min_bytes")] long? MinBytes = null,
    [property: JsonPropertyName("min_frac")] double? MinFrac = null,
    [property: JsonPropertyName("refresh_of")] string? RefreshOf = null)
{
    /// <summary>The argv after the sampler path. <c>RefreshOf</c> is for the server only.</summary>
    public IReadOnlyList<string> Arguments()
    {
        var args = new List<string> { "scan" };
        if (!string.IsNullOrEmpty(Root)) { args.Add("--root"); args.Add(Root); }
        if (MaxDepth is int d) { args.Add("--max-depth"); args.Add(d.ToString(CultureInfo.InvariantCulture)); }
        if (MinBytes is long b) { args.Add("--min-bytes"); args.Add(b.ToString(CultureInfo.InvariantCulture)); }
        if (MinFrac is double f) { args.Add("--min-frac"); args.Add(f.ToString(CultureInfo.InvariantCulture)); }
        return args;
    }
}

public sealed record DiskScanTop(
    [property: JsonPropertyName("path")] string Path,
    [property: JsonPropertyName("b")] long B);

public sealed record DiskScanProgress(
    [property: JsonPropertyName("t")] string T,
    [property: JsonPropertyName("v")] int V,
    [property: JsonPropertyName("files")] long Files,
    [property: JsonPropertyName("dirs")] long Dirs,
    [property: JsonPropertyName("bytes")] long Bytes,
    [property: JsonPropertyName("no_access_dirs")] long NoAccessDirs,
    [property: JsonPropertyName("current")] string Current,
    [property: JsonPropertyName("elapsed_ms")] long ElapsedMs,
    [property: JsonPropertyName("used_bytes")] long UsedBytes,
    [property: JsonPropertyName("top")] IReadOnlyList<DiskScanTop>? Top);

public sealed record DiskScanFs(
    [property: JsonPropertyName("mount")] string Mount,
    [property: JsonPropertyName("total")] long Total,
    [property: JsonPropertyName("used")] long Used,
    [property: JsonPropertyName("free")] long Free);

public sealed record DiskScanNoAccess(
    [property: JsonPropertyName("dirs")] IReadOnlyList<string>? Dirs,
    [property: JsonPropertyName("count")] long Count,
    [property: JsonPropertyName("bytes_est")] long? BytesEst);

/// <summary>One node of the pruned tree. <c>F</c>: file, <c>O</c>: the "other" node.</summary>
public sealed partial record DiskNode(
    [property: JsonPropertyName("n")] string N,
    [property: JsonPropertyName("b")] long B,
    [property: JsonPropertyName("cat")] string Cat,
    [property: JsonPropertyName("id")] string? Id = null,
    [property: JsonPropertyName("f")] bool F = false,
    [property: JsonPropertyName("o")] bool O = false,
    [property: JsonPropertyName("c")] IReadOnlyList<DiskNode>? C = null)
{
    [JsonIgnore]
    public IReadOnlyList<DiskNode> Children => C ?? Array.Empty<DiskNode>();
}

public sealed record DiskRecCmd(
    [property: JsonPropertyName("sh")] IReadOnlyList<string>? Sh,
    [property: JsonPropertyName("pwsh")] IReadOnlyList<string>? Pwsh);

public sealed partial record DiskRec(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("group")] string Group,
    [property: JsonPropertyName("label")] string Label,
    [property: JsonPropertyName("path")] string Path,
    [property: JsonPropertyName("bytes")] long Bytes,
    [property: JsonPropertyName("elevated")] bool Elevated,
    [property: JsonPropertyName("node_ids")] IReadOnlyList<string>? NodeIds,
    [property: JsonPropertyName("cmd")] DiskRecCmd Cmd)
{
    public const string SafeGroup = "safe";
    public const string ReviewGroup = "review";

    [JsonIgnore]
    public bool IsSafe => Group == SafeGroup;
}

/// <summary>The prune limits of a result (sampler §3.4). Older samplers leave them out.</summary>
public sealed record DiskScanLimits(
    [property: JsonPropertyName("max_depth")] int MaxDepth,
    [property: JsonPropertyName("min_bytes")] long MinBytes);

/// <summary>App side: a folder that the app scanned again and merged into the result.</summary>
public sealed record DiskScanRefresh(
    [property: JsonPropertyName("path")] string Path,
    [property: JsonPropertyName("at")] DateTime At);

public sealed partial record DiskScanResult(
    [property: JsonPropertyName("t")] string T,
    [property: JsonPropertyName("v")] int V,
    [property: JsonPropertyName("root")] string Root,
    [property: JsonPropertyName("os")] string Os,
    [property: JsonPropertyName("user")] string User,
    [property: JsonPropertyName("hostname")] string Hostname,
    [property: JsonPropertyName("home")] string Home,
    [property: JsonPropertyName("started_ts")] DateTime StartedTs,
    [property: JsonPropertyName("duration_ms")] long DurationMs,
    [property: JsonPropertyName("files")] long Files,
    [property: JsonPropertyName("dirs")] long Dirs,
    [property: JsonPropertyName("scanned_bytes")] long ScannedBytes,
    [property: JsonPropertyName("fs")] DiskScanFs? Fs,
    [property: JsonPropertyName("no_access")] DiskScanNoAccess NoAccess,
    [property: JsonPropertyName("tree")] DiskNode Tree,
    [property: JsonPropertyName("recs")] IReadOnlyList<DiskRec>? Recs,
    [property: JsonPropertyName("errors")] IReadOnlyList<string>? Errors,
    [property: JsonPropertyName("limits")] DiskScanLimits? Limits = null,
    [property: JsonPropertyName("refreshed")] IReadOnlyList<DiskScanRefresh>? Refreshed = null)
{
    [JsonIgnore]
    public IReadOnlyList<DiskRec> Recommendations => Recs ?? Array.Empty<DiskRec>();

    [JsonIgnore]
    public IReadOnlyList<DiskScanRefresh> RefreshedFolders => Refreshed ?? Array.Empty<DiskScanRefresh>();

    [JsonIgnore]
    public bool IsWindows => Os == "windows";

    [JsonIgnore]
    public DateTime FinishedAt => StartedTs.ToUniversalTime().AddMilliseconds(DurationMs);
}

/// <summary>One line of a scan, or a job update in remote mode.</summary>
public abstract record DiskScanEvent
{
    public sealed record Progress(DiskScanProgress Line) : DiskScanEvent;
    public sealed record Result(DiskScanResult Value) : DiskScanEvent;
    public sealed record Error(string Message) : DiskScanEvent;
    public sealed record Cancelled : DiskScanEvent;
    /// <summary>Remote mode only: the server job changed.</summary>
    public sealed record Job(DiskScanJob Value) : DiskScanEvent;

    /// <summary>True for the last event of a scan.</summary>
    public bool IsFinal => this is Result or Error or Cancelled;
}

/// <summary>Server job of a remote scan (docs/wire.md §3.1).</summary>
public sealed record DiskScanJob(
    [property: JsonPropertyName("id")] Guid Id,
    [property: JsonPropertyName("node_id")] Guid NodeId,
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("root")] string? Root = null,
    [property: JsonPropertyName("max_depth")] int? MaxDepth = null,
    [property: JsonPropertyName("min_bytes")] long? MinBytes = null,
    [property: JsonPropertyName("min_frac")] double? MinFrac = null,
    [property: JsonPropertyName("refresh_of")] string? RefreshOf = null,
    [property: JsonPropertyName("progress")] DiskScanProgress? Progress = null,
    [property: JsonPropertyName("error")] string? Error = null,
    [property: JsonPropertyName("cancel_requested")] bool CancelRequested = false,
    [property: JsonPropertyName("created_ts")] DateTime? CreatedTs = null,
    [property: JsonPropertyName("started_ts")] DateTime? StartedTs = null,
    [property: JsonPropertyName("finished_ts")] DateTime? FinishedTs = null,
    [property: JsonPropertyName("result_bytes")] long ResultBytes = 0)
{
    public const string Queued = "queued", Running = "running", Done = "done",
        Failed = "failed", CancelledState = "cancelled", Expired = "expired";

    [JsonIgnore]
    public bool IsFinal => State is Done or Failed or CancelledState or Expired;

    /// <summary>True for the scan of one folder. The server sends an empty string for a full scan.</summary>
    [JsonIgnore]
    public bool IsRefresh => !string.IsNullOrEmpty(RefreshOf);
}

/// <summary>Decodes the NDJSON lines of <c>towertail-sampler scan</c>.</summary>
public static class DiskScanCodec
{
    public const int SupportedVersion = 1;

    public static JsonSerializerOptions Options { get; } = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        NumberHandling = JsonNumberHandling.AllowReadingFromString,
    };

    private sealed record Head(
        [property: JsonPropertyName("t")] string? T,
        [property: JsonPropertyName("v")] int V,
        [property: JsonPropertyName("message")] string? Message);

    /// <summary>Decode one line. Throws <see cref="JsonException"/> on a bad or unknown line.</summary>
    public static DiskScanEvent DecodeLine(string line)
    {
        var head = JsonSerializer.Deserialize<Head>(line, Options) ?? throw new JsonException("empty scan line");
        if (head.V <= 0) throw new JsonException($"missing or invalid 'v' (got {head.V})");
        if (head.V > SupportedVersion)
            throw new JsonException($"scan line v={head.V} is newer than supported v={SupportedVersion}");
        return head.T switch
        {
            "progress" => new DiskScanEvent.Progress(Deserialize<DiskScanProgress>(line)),
            "result" => new DiskScanEvent.Result(Deserialize<DiskScanResult>(line)),
            "error" => new DiskScanEvent.Error(head.Message ?? "scan failed"),
            "cancelled" => new DiskScanEvent.Cancelled(),
            _ => throw new JsonException($"unknown scan line type '{head.T}'"),
        };
    }

    public static DiskScanResult DecodeResult(string json)
        => DecodeLine(json) is DiskScanEvent.Result r ? r.Value : throw new JsonException("not a result line");

    public static string Encode(DiskScanResult result) => JsonSerializer.Serialize(result, Options);

    private static T Deserialize<T>(string line)
        => JsonSerializer.Deserialize<T>(line, Options) ?? throw new JsonException("empty scan line");
}

/// <summary>Size text for the Usage tab and the cleanup script. Powers of 1000, as the sampler.</summary>
public static class DiskSize
{
    public static string Format(long bytes)
    {
        var b = Math.Max(0, bytes);
        var ci = CultureInfo.InvariantCulture;
        if (b >= 1_000_000_000_000) return (b / 1e12).ToString("0.0", ci) + " TB";
        if (b >= 1_000_000_000) return (b / 1e9).ToString("0.0", ci) + " GB";
        if (b >= 1_000_000) return (b / 1e6).ToString("0", ci) + " MB";
        if (b >= 1_000) return (b / 1e3).ToString("0", ci) + " KB";
        return b.ToString(ci) + " B";
    }
}
