using System.Collections.Immutable;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

public enum AlertMetric { Cpu, Mem, Disk, Health }

/// <summary>Which levels of one metric send a notification. Wire values match the Mac.</summary>
public enum AlertNotify { Off, Critical, All }

public sealed record AlertRule(int SustainSeconds, AlertNotify Notify)
{
    public const int MaxSustainSeconds = 3600;

    public AlertRule Clamped() => this with { SustainSeconds = Math.Clamp(SustainSeconds, 0, MaxSustainSeconds) };

    public bool Allows(HealthLevel level) => level switch
    {
        HealthLevel.Critical => Notify != AlertNotify.Off,
        HealthLevel.Warn => Notify == AlertNotify.All,
        _ => false,
    };
}

/// <summary>
/// Per-metric sustain and notify rules (settings.json <c>thresholds.alerts</c> and
/// <c>nodes[].customAlerts</c>). Mirrors AlertRules.swift.
/// </summary>
[JsonConverter(typeof(AlertRulesJsonConverter))]
public sealed record AlertRules(AlertRule Cpu, AlertRule Mem, AlertRule Disk, AlertRule Health, double Tolerance)
{
    public const double DefaultTolerance = 0.8;

    public static readonly AlertRule DefaultCpu = new(300, AlertNotify.Critical);
    public static readonly AlertRule DefaultMem = new(120, AlertNotify.All);
    public static readonly AlertRule DefaultDisk = new(0, AlertNotify.All);
    public static readonly AlertRule DefaultHealth = new(300, AlertNotify.All);

    public static AlertRules Defaults => new(DefaultCpu, DefaultMem, DefaultDisk, DefaultHealth, DefaultTolerance);

    public AlertRule For(AlertMetric m) => m switch
    {
        AlertMetric.Cpu => Cpu,
        AlertMetric.Mem => Mem,
        AlertMetric.Disk => Disk,
        _ => Health,
    };

    public AlertRules With(AlertMetric m, AlertRule rule) => m switch
    {
        AlertMetric.Cpu => this with { Cpu = rule.Clamped() },
        AlertMetric.Mem => this with { Mem = rule.Clamped() },
        AlertMetric.Disk => this with { Disk = rule.Clamped() },
        _ => this with { Health = rule.Clamped() },
    };

    public AlertRules Clamped() => new(Cpu.Clamped(), Mem.Clamped(), Disk.Clamped(), Health.Clamped(),
        Math.Clamp(Tolerance, 0.5, 1.0));

    public static AlertRule DefaultFor(AlertMetric m) => Defaults.For(m);

    /// <summary>
    /// Rules for a file written before <c>alerts</c> existed. A legacy sample count above 1
    /// becomes a duration of samples × SSH poll interval; otherwise the new default applies.
    /// </summary>
    public static AlertRules Migrate(int? cpuSamples, int? memSamples, int? diskSamples, int sshPollSeconds)
    {
        AlertRule Conv(AlertRule d, int? samples)
            => samples is > 1 ? (d with { SustainSeconds = samples.Value * sshPollSeconds }).Clamped() : d;
        var d = Defaults;
        return d with { Cpu = Conv(d.Cpu, cpuSamples), Mem = Conv(d.Mem, memSamples), Disk = Conv(d.Disk, diskSamples) };
    }
}

/// <summary>Missing keys fall back to the default of their own metric, which plain binding cannot do.</summary>
public sealed class AlertRulesJsonConverter : JsonConverter<AlertRules>
{
    private static readonly (string Key, AlertMetric Metric)[] Keys =
    {
        ("cpu", AlertMetric.Cpu), ("mem", AlertMetric.Mem), ("disk", AlertMetric.Disk), ("health", AlertMetric.Health),
    };

    public override AlertRules Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        var obj = JsonNode.Parse(ref reader) as JsonObject ?? throw new JsonException("alerts must be an object");
        var rules = AlertRules.Defaults;
        foreach (var (key, metric) in Keys)
        {
            if (obj[key] is not JsonObject r) continue;
            var d = AlertRules.DefaultFor(metric);
            var sustain = TryInt(r["sustainSeconds"]) ?? d.SustainSeconds;
            var notify = ParseNotify(TryString(r["notify"])) ?? d.Notify;
            rules = rules.With(metric, new AlertRule(sustain, notify));
        }
        var tol = TryDouble(obj["tolerance"]) ?? AlertRules.DefaultTolerance;
        return (rules with { Tolerance = tol }).Clamped();
    }

    public override void Write(Utf8JsonWriter writer, AlertRules value, JsonSerializerOptions options)
    {
        writer.WriteStartObject();
        foreach (var (key, metric) in Keys)
        {
            var r = value.For(metric);
            writer.WriteStartObject(key);
            writer.WriteNumber("sustainSeconds", r.SustainSeconds);
            writer.WriteString("notify", NotifyName(r.Notify));
            writer.WriteEndObject();
        }
        writer.WriteNumber("tolerance", value.Tolerance);
        writer.WriteEndObject();
    }

    internal static string NotifyName(AlertNotify n) => n switch
    {
        AlertNotify.Off => "off",
        AlertNotify.Critical => "critical",
        _ => "all",
    };

    internal static AlertNotify? ParseNotify(string? s) => s switch
    {
        "off" => AlertNotify.Off,
        "critical" => AlertNotify.Critical,
        "all" => AlertNotify.All,
        _ => null,
    };

    internal static int? TryInt(JsonNode? n)
        => n is JsonValue v && v.TryGetValue<double>(out var d) ? (int)Math.Round(d) : null;

    internal static double? TryDouble(JsonNode? n)
        => n is JsonValue v && v.TryGetValue<double>(out var d) ? d : null;

    internal static string? TryString(JsonNode? n)
        => n is JsonValue v && v.TryGetValue<string>(out var s) ? s : null;
}

/// <summary>
/// Finds one process that uses a lot of CPU for a long time (settings.json <c>longProc</c>
/// and <c>nodes[].customLongProc</c>). <c>CpuPct</c> uses the <c>top</c> convention: 100 is
/// one full core. Mirrors LongProcRule in the Mac app and the server.
/// </summary>
[JsonConverter(typeof(LongProcRuleJsonConverter))]
public sealed record LongProcRule
{
    /// <summary>Fraction of the window that must be high. Fixed in all products.</summary>
    public const double Tolerance = 0.8;
    public const double MinCpuPct = 10, MaxCpuPct = 800;
    public const int MinSustainSeconds = 60, MaxSustainSeconds = 86_400;

    public bool Enabled { get; init; } = true;
    public double CpuPct { get; init; } = 50;
    public int SustainSeconds { get; init; } = 1800;
    /// <summary>Only Off and All apply: a long process is a warning.</summary>
    public AlertNotify Notify { get; init; } = AlertNotify.All;
    /// <summary>Process names, matched exactly without case.</summary>
    public IReadOnlyList<string> Ignore { get; init; } = Array.Empty<string>();

    public static LongProcRule Defaults => new();

    public LongProcRule Clamped() => this with
    {
        CpuPct = Math.Clamp(CpuPct, MinCpuPct, MaxCpuPct),
        SustainSeconds = Math.Clamp(SustainSeconds, MinSustainSeconds, MaxSustainSeconds),
    };

    public bool Notifies => Notify != AlertNotify.Off;

    public string LogValue => Enabled ? $"{(int)CpuPct}%/{SustainSeconds}s/{AlertRulesJsonConverter.NotifyName(Notify)}" : "off";

    /// <summary>Lowercase union of the rule list and the other lists.</summary>
    public IReadOnlySet<string> IgnoreSet(params IEnumerable<string>[] other)
        => Ignore.Concat(other.SelectMany(o => o)).Select(n => n.ToLowerInvariant()).ToHashSet();

    public bool Equals(LongProcRule? other)
        => other is not null && Enabled == other.Enabled && CpuPct == other.CpuPct
           && SustainSeconds == other.SustainSeconds && Notify == other.Notify && Ignore.SequenceEqual(other.Ignore);

    public override int GetHashCode() => HashCode.Combine(Enabled, CpuPct, SustainSeconds, Notify, Ignore.Count);
}

/// <summary>Each field decodes on its own, so one bad value keeps the defaults of the others.</summary>
public sealed class LongProcRuleJsonConverter : JsonConverter<LongProcRule>
{
    public override LongProcRule Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        if (JsonNode.Parse(ref reader) is not JsonObject obj) return LongProcRule.Defaults;
        var d = LongProcRule.Defaults;
        return new LongProcRule
        {
            Enabled = obj["enabled"] is JsonValue e && e.TryGetValue<bool>(out var b) ? b : d.Enabled,
            CpuPct = AlertRulesJsonConverter.TryDouble(obj["cpuPct"]) ?? d.CpuPct,
            SustainSeconds = AlertRulesJsonConverter.TryInt(obj["sustainSeconds"]) ?? d.SustainSeconds,
            Notify = AlertRulesJsonConverter.ParseNotify(AlertRulesJsonConverter.TryString(obj["notify"])) ?? d.Notify,
            Ignore = obj["ignore"] is JsonArray a
                ? a.Select(AlertRulesJsonConverter.TryString).OfType<string>().ToArray()
                : d.Ignore,
        }.Clamped();
    }

    public override void Write(Utf8JsonWriter writer, LongProcRule value, JsonSerializerOptions options)
    {
        writer.WriteStartObject();
        writer.WriteBoolean("enabled", value.Enabled);
        writer.WriteNumber("cpuPct", value.CpuPct);
        writer.WriteNumber("sustainSeconds", value.SustainSeconds);
        writer.WriteString("notify", AlertRulesJsonConverter.NotifyName(value.Notify));
        writer.WriteStartArray("ignore");
        foreach (var n in value.Ignore) writer.WriteStringValue(n);
        writer.WriteEndArray();
        writer.WriteEndObject();
    }
}

/// <summary>
/// Time-based sustain gate for one metric. Each point holds its level until the next point
/// (sample-and-hold). The alert level is the highest level held for at least
/// <c>tolerance</c> of the sustain window. Mirrors SustainWindow in the Mac client.
/// </summary>
public sealed class SustainWindow
{
    /// <summary>A gap longer than the slowest poll interval means sleep or an outage: start again.</summary>
    public static readonly TimeSpan MaxGap = TimeSpan.FromMinutes(5);

    private readonly List<(DateTime T, HealthLevel Level)> _points = new();

    public void Record(DateTime t, HealthLevel level, int sustainSeconds)
    {
        if (_points.Count > 0 && t - _points[^1].T > MaxGap) _points.Clear();
        _points.Add((t, level));
        // Keep one anchor point at or before the cutoff so the window start is covered.
        var cutoff = t - TimeSpan.FromSeconds(Math.Max(0, sustainSeconds));
        while (_points.Count > 1 && _points[1].T <= cutoff) _points.RemoveAt(0);
    }

    public void Reset() => _points.Clear();

    public HealthLevel Level(int sustainSeconds, double tolerance)
    {
        if (_points.Count == 0) return HealthLevel.Nominal;
        if (sustainSeconds <= 0) return _points[^1].Level;

        var now = _points[^1].T;
        var cutoff = now - TimeSpan.FromSeconds(sustainSeconds);
        if (_points[0].T > cutoff) return HealthLevel.Nominal;

        double warn = 0, critical = 0;
        for (int i = 0; i < _points.Count - 1; i++)
        {
            var start = _points[i].T < cutoff ? cutoff : _points[i].T;
            var d = (_points[i + 1].T - start).TotalSeconds;
            if (d <= 0) continue;
            if (_points[i].Level >= HealthLevel.Warn) warn += d;
            if (_points[i].Level == HealthLevel.Critical) critical += d;
        }
        if (critical / sustainSeconds >= tolerance) return HealthLevel.Critical;
        if (warn / sustainSeconds >= tolerance) return HealthLevel.Warn;
        return HealthLevel.Nominal;
    }
}

/// <summary>A signal that the user can mute. Wire names match ThresholdMetric on the Mac.</summary>
public enum AlertSignal { Cpu, Mem, Disk, Procs, Zombies, Pids, Files, Inodes, MemPressure, IoPressure }

/// <summary>
/// Signals that send no notification and do not tint the tray icon
/// (settings.json <c>thresholds.muted</c> and <c>nodes[].mutedSignals</c>). Mirrors MutedSignals on the Mac.
/// </summary>
[JsonConverter(typeof(MutedSignalsJsonConverter))]
public sealed record MutedSignals(ImmutableHashSet<AlertSignal> Set)
{
    public static readonly MutedSignals None = new(ImmutableHashSet<AlertSignal>.Empty);

    public MutedSignals(IEnumerable<AlertSignal> signals) : this(signals.ToImmutableHashSet()) { }

    public bool IsEmpty => Set.IsEmpty;

    public bool Contains(AlertSignal s) => Set.Contains(s);

    /// <summary>Only CPU, memory and disk. Health is filtered per reason.</summary>
    public bool Mutes(AlertMetric m) => m switch
    {
        AlertMetric.Cpu => Set.Contains(AlertSignal.Cpu),
        AlertMetric.Mem => Set.Contains(AlertSignal.Mem),
        AlertMetric.Disk => Set.Contains(AlertSignal.Disk),
        _ => false,
    };

    public bool Mutes(HealthSignal s) => s.ToAlertSignal() is { } a && Set.Contains(a);

    public MutedSignals With(AlertSignal s, bool muted) => new(muted ? Set.Add(s) : Set.Remove(s));

    public static MutedSignals Effective(MutedSignals global, MutedSignals node)
        => node.IsEmpty ? global : global.IsEmpty ? node : new(global.Set.Union(node.Set));

    public static string Name(AlertSignal s)
    {
        var n = s.ToString();
        return char.ToLowerInvariant(n[0]) + n[1..];
    }

    public static AlertSignal? Parse(string? name)
        => name is { Length: > 0 } && char.IsLower(name[0])
           && Enum.TryParse<AlertSignal>(char.ToUpperInvariant(name[0]) + name[1..], out var s) ? s : null;

    // Record equality compares the set by reference.
    public bool Equals(MutedSignals? other) => other is not null && Set.SetEquals(other.Set);

    public override int GetHashCode() => Set.Aggregate(0, (h, s) => h ^ (1 << (int)s));
}

/// <summary>
/// Reads a string array and skips unknown names. A bad value reads as <see cref="MutedSignals.None"/>,
/// so one bad value does not drop all settings. Writes the names in sorted order.
/// </summary>
public sealed class MutedSignalsJsonConverter : JsonConverter<MutedSignals>
{
    public override MutedSignals Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        if (reader.TokenType != JsonTokenType.StartArray)
        {
            reader.Skip();
            return MutedSignals.None;
        }
        var set = ImmutableHashSet.CreateBuilder<AlertSignal>();
        while (reader.Read() && reader.TokenType != JsonTokenType.EndArray)
        {
            if (reader.TokenType == JsonTokenType.String && MutedSignals.Parse(reader.GetString()) is { } s)
                set.Add(s);
            else
                reader.Skip();
        }
        return new MutedSignals(set.ToImmutable());
    }

    public override void Write(Utf8JsonWriter writer, MutedSignals value, JsonSerializerOptions options)
    {
        writer.WriteStartArray();
        foreach (var n in value.Set.Select(MutedSignals.Name).OrderBy(n => n, StringComparer.Ordinal))
            writer.WriteStringValue(n);
        writer.WriteEndArray();
    }
}
