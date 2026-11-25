using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

/// <summary>Warn and critical levels for one metric. Units depend on the metric.</summary>
public sealed record ThresholdPair(
    [property: JsonPropertyName("warn")] double Warn,
    [property: JsonPropertyName("critical")] double Critical)
{
    /// <summary>Copy with critical not below warn.</summary>
    public ThresholdPair Clamped() => this with { Critical = Math.Max(Warn, Critical) };
}

/// <summary>
/// Host health limits other than process and zombie counts. PID use, open files and inodes are
/// fractions. Linux pressure is PSI avg10 percent. macOS pressure is the percent of memory in use.
/// A missing JSON key gets the default.
/// </summary>
public sealed record HealthLimits
{
    [JsonPropertyName("pids")]
    public ThresholdPair Pids { get; init; } = new(0.70, 0.90);
    [JsonPropertyName("files")]
    public ThresholdPair Files { get; init; } = new(0.80, 0.95);
    [JsonPropertyName("inodes")]
    public ThresholdPair Inodes { get; init; } = new(0.85, 0.95);
    /// <summary>Linux PSI avg10. Warns on some, goes critical on full.</summary>
    [JsonPropertyName("memPressure")]
    public ThresholdPair MemPressure { get; init; } = new(10, 20);
    [JsonPropertyName("ioPressure")]
    public ThresholdPair IoPressure { get; init; } = new(20, 40);
    /// <summary>macOS memory in use, in percent.</summary>
    [JsonPropertyName("macMemPressure")]
    public ThresholdPair MacMemPressure { get; init; } = new(80, 90);

    public static HealthLimits Defaults => new();
}

/// <summary>Metrics that have a warn/critical pair a node can override.</summary>
public enum ThresholdMetric { Cpu, Mem, Disk, Procs, Zombies, Pids, Files, Inodes, MemPressure, IoPressure }

public static class ThresholdMetrics
{
    public static readonly IReadOnlyList<ThresholdMetric> All = Enum.GetValues<ThresholdMetric>();
    public static readonly IReadOnlyList<ThresholdMetric> Usage = new[] { ThresholdMetric.Cpu, ThresholdMetric.Mem, ThresholdMetric.Disk };
    public static readonly IReadOnlyList<ThresholdMetric> Health = All.Skip(3).ToArray();

    /// <summary>The wire name: the metric in camelCase.</summary>
    public static string Key(this ThresholdMetric m)
    {
        var n = m.ToString();
        return char.ToLowerInvariant(n[0]) + n[1..];
    }

    /// <summary>The mute signal of the metric. Both enums use the same names.</summary>
    public static AlertSignal Signal(this ThresholdMetric m) => Enum.Parse<AlertSignal>(m.ToString());

    public static string Title(this ThresholdMetric m) => m switch
    {
        ThresholdMetric.Cpu => "CPU",
        ThresholdMetric.Mem => "Memory",
        ThresholdMetric.Disk => "Disk",
        ThresholdMetric.Procs => "Processes",
        ThresholdMetric.Zombies => "Zombies",
        ThresholdMetric.Pids => "PID use",
        ThresholdMetric.Files => "Open files",
        ThresholdMetric.Inodes => "Inodes",
        ThresholdMetric.MemPressure => "Memory pressure",
        _ => "I/O pressure",
    };

    /// <summary>Fraction metrics are stored as 0..1 and shown as percent.</summary>
    public static bool IsFraction(this ThresholdMetric m)
        => m is ThresholdMetric.Cpu or ThresholdMetric.Mem or ThresholdMetric.Disk
            or ThresholdMetric.Pids or ThresholdMetric.Files or ThresholdMetric.Inodes;

    public static bool IsCount(this ThresholdMetric m) => m is ThresholdMetric.Procs or ThresholdMetric.Zombies;

    /// <summary>Factor from the stored value to the value the user edits.</summary>
    public static double Scale(this ThresholdMetric m) => m.IsFraction() ? 100 : 1;

    public static string Format(this ThresholdMetric m, double v)
        => m.IsCount() ? ((int)v).ToString("N0", System.Globalization.CultureInfo.InvariantCulture)
            : $"{(int)Math.Round(v * m.Scale())}%";
}

/// <summary>
/// Per-node threshold overrides. A null metric uses the global value. The JSON keys match the
/// Mac <c>ThresholdOverrides</c>.
/// </summary>
public sealed record ThresholdOverrides(
    [property: JsonPropertyName("cpu"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Cpu = null,
    [property: JsonPropertyName("mem"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Mem = null,
    [property: JsonPropertyName("disk"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Disk = null,
    [property: JsonPropertyName("procs"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Procs = null,
    [property: JsonPropertyName("zombies"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Zombies = null,
    [property: JsonPropertyName("pids"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Pids = null,
    [property: JsonPropertyName("files"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Files = null,
    [property: JsonPropertyName("inodes"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? Inodes = null,
    [property: JsonPropertyName("memPressure"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? MemPressure = null,
    [property: JsonPropertyName("ioPressure"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] ThresholdPair? IoPressure = null)
{
    public ThresholdPair? Get(ThresholdMetric m) => m switch
    {
        ThresholdMetric.Cpu => Cpu,
        ThresholdMetric.Mem => Mem,
        ThresholdMetric.Disk => Disk,
        ThresholdMetric.Procs => Procs,
        ThresholdMetric.Zombies => Zombies,
        ThresholdMetric.Pids => Pids,
        ThresholdMetric.Files => Files,
        ThresholdMetric.Inodes => Inodes,
        ThresholdMetric.MemPressure => MemPressure,
        _ => IoPressure,
    };

    /// <summary>Copy with one metric set, or cleared when <paramref name="pair"/> is null.</summary>
    public ThresholdOverrides With(ThresholdMetric m, ThresholdPair? pair) => m switch
    {
        ThresholdMetric.Cpu => this with { Cpu = pair },
        ThresholdMetric.Mem => this with { Mem = pair },
        ThresholdMetric.Disk => this with { Disk = pair },
        ThresholdMetric.Procs => this with { Procs = pair },
        ThresholdMetric.Zombies => this with { Zombies = pair },
        ThresholdMetric.Pids => this with { Pids = pair },
        ThresholdMetric.Files => this with { Files = pair },
        ThresholdMetric.Inodes => this with { Inodes = pair },
        ThresholdMetric.MemPressure => this with { MemPressure = pair },
        _ => this with { IoPressure = pair },
    };

    [JsonIgnore]
    public bool IsEmpty => ThresholdMetrics.All.All(m => Get(m) is null);

    /// <summary>The global thresholds with each overridden metric replaced.</summary>
    public MetricThresholds Applied(MetricThresholds global)
    {
        var t = global;
        foreach (var m in ThresholdMetrics.All)
            if (Get(m) is { } pair) t = t.With(m, pair);
        // One host has one OS, so the one key sets both pressure pairs.
        if (MemPressure is { } p) t = t with { Health = t.HealthOrDefault with { MacMemPressure = p.Clamped() } };
        return t;
    }

    /// <summary>The old all-or-nothing override set CPU, memory and disk only.</summary>
    public static ThresholdOverrides FromLegacy(MetricThresholds custom) => new(
        Cpu: new ThresholdPair(custom.CpuWarn, custom.CpuCritical),
        Mem: new ThresholdPair(custom.MemWarn, custom.MemCritical),
        Disk: new ThresholdPair(custom.DiskWarn, custom.DiskCritical));
}

/// <summary>
/// The thresholds as the server sends them: six usage values, four health counts and
/// <c>health</c>. A missing or bad key gets the default, as on the server.
/// </summary>
public sealed class MetricThresholdsJsonConverter : JsonConverter<MetricThresholds>
{
    public override MetricThresholds Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        var obj = JsonNode.Parse(ref reader) as JsonObject ?? throw new JsonException("thresholds must be an object");
        var d = MetricThresholds.Defaults;
        double Num(string key, double fallback) => AlertRulesJsonConverter.TryDouble(obj[key]) ?? fallback;
        HealthLimits? health = null;
        try { health = obj["health"]?.Deserialize<HealthLimits>(options); } catch (JsonException) { }
        return new MetricThresholds(
            Num("cpuWarn", d.CpuWarn), Num("cpuCritical", d.CpuCritical),
            Num("memWarn", d.MemWarn), Num("memCritical", d.MemCritical),
            Num("diskWarn", d.DiskWarn), Num("diskCritical", d.DiskCritical))
            .WithHealth(
                (int)Num("procsWarn", d.ProcsWarn), (int)Num("procsCritical", d.ProcsCritical),
                (int)Num("zombiesWarn", d.ZombiesWarn), (int)Num("zombiesCritical", d.ZombiesCritical))
            with { Health = health };
    }

    public override void Write(Utf8JsonWriter writer, MetricThresholds value, JsonSerializerOptions options)
    {
        writer.WriteStartObject();
        writer.WriteNumber("cpuWarn", value.CpuWarn);
        writer.WriteNumber("cpuCritical", value.CpuCritical);
        writer.WriteNumber("memWarn", value.MemWarn);
        writer.WriteNumber("memCritical", value.MemCritical);
        writer.WriteNumber("diskWarn", value.DiskWarn);
        writer.WriteNumber("diskCritical", value.DiskCritical);
        writer.WriteNumber("procsWarn", value.ProcsWarn);
        writer.WriteNumber("procsCritical", value.ProcsCritical);
        writer.WriteNumber("zombiesWarn", value.ZombiesWarn);
        writer.WriteNumber("zombiesCritical", value.ZombiesCritical);
        writer.WritePropertyName("health");
        JsonSerializer.Serialize(writer, value.HealthOrDefault, options);
        writer.WriteEndObject();
    }
}
