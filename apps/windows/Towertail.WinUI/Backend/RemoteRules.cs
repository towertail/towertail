using System.Globalization;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

// The rules model of the server (docs/wire.md: HostRules, OrgRules, EffectiveRules,
// RulesView, RulesImport). Mirrors RemoteRules.swift on the Mac.

/// <summary>Reads an object key by key. A missing or bad key gets the fallback, as on the server.</summary>
public abstract class LenientObjectConverter<T> : JsonConverter<T>
{
    public sealed override T Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        var obj = JsonNode.Parse(ref reader) as JsonObject ?? throw new JsonException($"{typeof(T).Name} must be an object");
        return Parse(obj, options);
    }

    protected abstract T Parse(JsonObject obj, JsonSerializerOptions options);

    protected static V Get<V>(JsonObject obj, string key, JsonSerializerOptions options, V fallback)
    {
        try { return obj[key] is { } node && node.Deserialize<V>(options) is { } v ? v : fallback; }
        catch (Exception ex) when (ex is JsonException or InvalidOperationException) { return fallback; }
    }

    protected static V? GetOrNull<V>(JsonObject obj, string key, JsonSerializerOptions options) where V : class
    {
        try { return obj[key]?.Deserialize<V>(options); }
        catch (Exception ex) when (ex is JsonException or InvalidOperationException) { return null; }
    }

    protected static void Put<V>(Utf8JsonWriter w, string key, V value, JsonSerializerOptions options)
    {
        w.WritePropertyName(key);
        JsonSerializer.Serialize(w, value, options);
    }
}

/// <summary>The client that set rules last. The server sets <c>UserId</c> and <c>At</c>.</summary>
[JsonConverter(typeof(RemoteRulesOriginJsonConverter))]
public sealed record RemoteRulesOrigin(string Client, string? Device = null, Guid? UserId = null, DateTimeOffset? At = null)
{
    /// <summary>The device that set the rules, or the client name.</summary>
    public string Source => Device ?? Client;
}

public sealed class RemoteRulesOriginJsonConverter : LenientObjectConverter<RemoteRulesOrigin>
{
    protected override RemoteRulesOrigin Parse(JsonObject obj, JsonSerializerOptions options)
    {
        // The time has fractional seconds. A bad time does not fail the node.
        var at = obj["at"] is JsonValue v && v.TryGetValue<string>(out var text)
                 && DateTimeOffset.TryParse(text, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind, out var t)
            ? t : (DateTimeOffset?)null;
        var user = obj["userId"] is JsonValue u && u.TryGetValue<string>(out var us) && Guid.TryParse(us, out var g) ? g : (Guid?)null;
        return new RemoteRulesOrigin(
            AlertRulesJsonConverter.TryString(obj["client"]) ?? "api",
            AlertRulesJsonConverter.TryString(obj["device"]), user, at);
    }

    /// <summary>The server takes only <c>client</c> and <c>device</c> from a request.</summary>
    public override void Write(Utf8JsonWriter writer, RemoteRulesOrigin value, JsonSerializerOptions options)
    {
        writer.WriteStartObject();
        writer.WriteString("client", value.Client);
        if (value.Device is not null) writer.WriteString("device", value.Device);
        writer.WriteEndObject();
    }
}

/// <summary>The alert timing that a host replaces. A null field uses the org rule.</summary>
public sealed record RemoteAlertOverrides(
    [property: JsonPropertyName("cpu"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] AlertRule? Cpu = null,
    [property: JsonPropertyName("mem"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] AlertRule? Mem = null,
    [property: JsonPropertyName("disk"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] AlertRule? Disk = null,
    [property: JsonPropertyName("health"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] AlertRule? Health = null,
    [property: JsonPropertyName("tolerance"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] double? Tolerance = null)
{
    [JsonIgnore]
    public bool IsEmpty => this == new RemoteAlertOverrides();

    public AlertRule? Rule(AlertMetric m) => m switch
    {
        AlertMetric.Cpu => Cpu,
        AlertMetric.Mem => Mem,
        AlertMetric.Disk => Disk,
        _ => Health,
    };

    /// <summary>Copy with one rule set, or cleared when <paramref name="rule"/> is null.</summary>
    public RemoteAlertOverrides With(AlertMetric m, AlertRule? rule) => m switch
    {
        AlertMetric.Cpu => this with { Cpu = rule },
        AlertMetric.Mem => this with { Mem = rule },
        AlertMetric.Disk => this with { Disk = rule },
        _ => this with { Health = rule },
    };

    public AlertRules Applied(AlertRules global)
    {
        var r = global;
        foreach (var m in AlertMetrics.All)
            if (Rule(m) is { } rule) r = r.With(m, rule);
        return Tolerance is { } t ? r with { Tolerance = t } : r;
    }

    /// <summary>Only the rules of <paramref name="host"/> that differ from <paramref name="global"/>.</summary>
    public static RemoteAlertOverrides Diff(AlertRules host, AlertRules global) => new(
        host.Cpu == global.Cpu ? null : host.Cpu,
        host.Mem == global.Mem ? null : host.Mem,
        host.Disk == global.Disk ? null : host.Disk,
        host.Health == global.Health ? null : host.Health,
        host.Tolerance == global.Tolerance ? null : host.Tolerance);
}

/// <summary>The org rules.</summary>
[JsonConverter(typeof(RemoteOrgRulesJsonConverter))]
public sealed record RemoteOrgRules
{
    public MetricThresholds Thresholds { get; init; } = MetricThresholds.Defaults;
    public AlertRules Alerts { get; init; } = AlertRules.Defaults;
    public MutedSignals Muted { get; init; } = MutedSignals.None;
    public LongProcRule LongProc { get; init; } = LongProcRule.Defaults;
    public RemoteRulesOrigin? Origin { get; init; }
}

public sealed class RemoteOrgRulesJsonConverter : LenientObjectConverter<RemoteOrgRules>
{
    protected override RemoteOrgRules Parse(JsonObject obj, JsonSerializerOptions o) => new()
    {
        Thresholds = Get(obj, "thresholds", o, MetricThresholds.Defaults),
        Alerts = Get(obj, "alerts", o, AlertRules.Defaults),
        Muted = Get(obj, "muted", o, MutedSignals.None),
        LongProc = Get(obj, "longProc", o, LongProcRule.Defaults),
        Origin = GetOrNull<RemoteRulesOrigin>(obj, "origin", o),
    };

    public override void Write(Utf8JsonWriter w, RemoteOrgRules v, JsonSerializerOptions o)
    {
        w.WriteStartObject();
        Put(w, "thresholds", v.Thresholds, o);
        Put(w, "alerts", v.Alerts, o);
        Put(w, "muted", v.Muted, o);
        Put(w, "longProc", v.LongProc, o);
        if (v.Origin is not null) Put(w, "origin", v.Origin, o);
        w.WriteEndObject();
    }
}

/// <summary>The rules that one host sets. A null field uses the org rule.</summary>
[JsonConverter(typeof(RemoteHostRulesJsonConverter))]
public sealed record RemoteHostRules
{
    private readonly ThresholdOverrides? _thresholds;
    private readonly RemoteAlertOverrides? _alerts;

    /// <summary>An empty set is null.</summary>
    public ThresholdOverrides? Thresholds { get => _thresholds; init => _thresholds = value is { IsEmpty: true } ? null : value; }
    public RemoteAlertOverrides? Alerts { get => _alerts; init => _alerts = value is { IsEmpty: true } ? null : value; }
    public MutedSignals Muted { get; init; } = MutedSignals.None;
    public LongProcRule? LongProc { get; init; }
    public IReadOnlyList<string> IgnoredProcs { get; init; } = Array.Empty<string>();
    public RemoteRulesOrigin? Origin { get; init; }

    /// <summary>The rules inside a node of the server: <c>thresholdOverrides</c>, <c>alertOverrides</c>, <c>mutedSignals</c>, <c>customLongProc</c>, <c>ignoredProcs</c> and <c>rulesOrigin</c>.</summary>
    public static RemoteHostRules FromNode(JsonObject node)
    {
        var o = RemoteClient.WireJson;
        var rules = new JsonObject();
        foreach (var (from, to) in new[]
            { ("thresholdOverrides", "thresholds"), ("alertOverrides", "alerts"), ("mutedSignals", "muted"),
              ("customLongProc", "longProc"), ("ignoredProcs", "ignoredProcs"), ("rulesOrigin", "origin") })
            if (node[from] is { } value) rules[to] = value.DeepClone();
        return rules.Deserialize<RemoteHostRules>(o) ?? new RemoteHostRules();
    }
}

public sealed class RemoteHostRulesJsonConverter : LenientObjectConverter<RemoteHostRules>
{
    protected override RemoteHostRules Parse(JsonObject obj, JsonSerializerOptions o) => new()
    {
        Thresholds = GetOrNull<ThresholdOverrides>(obj, "thresholds", o),
        Alerts = GetOrNull<RemoteAlertOverrides>(obj, "alerts", o),
        Muted = Get(obj, "muted", o, MutedSignals.None),
        LongProc = GetOrNull<LongProcRule>(obj, "longProc", o),
        IgnoredProcs = Get<string[]>(obj, "ignoredProcs", o, []),
        Origin = GetOrNull<RemoteRulesOrigin>(obj, "origin", o),
    };

    /// <summary>
    /// A body must always carry <c>muted</c>. The server clears an override that the body
    /// leaves out, so a null field is not sent.
    /// </summary>
    public override void Write(Utf8JsonWriter w, RemoteHostRules v, JsonSerializerOptions o)
    {
        w.WriteStartObject();
        if (v.Thresholds is not null) Put(w, "thresholds", v.Thresholds, o);
        if (v.Alerts is not null) Put(w, "alerts", v.Alerts, o);
        Put(w, "muted", v.Muted, o);
        if (v.LongProc is not null) Put(w, "longProc", v.LongProc, o);
        if (v.IgnoredProcs.Count > 0) Put(w, "ignoredProcs", v.IgnoredProcs, o);
        if (v.Origin is not null) Put(w, "origin", v.Origin, o);
        w.WriteEndObject();
    }
}

/// <summary><c>org</c> or <c>host</c> for each rule of a host.</summary>
[JsonConverter(typeof(RemoteRuleSourcesJsonConverter))]
public sealed record RemoteRuleSources
{
    public IReadOnlyDictionary<string, string> Thresholds { get; init; } = new Dictionary<string, string>();
    public IReadOnlyDictionary<string, string> Alerts { get; init; } = new Dictionary<string, string>();
    public IReadOnlyDictionary<string, string> Muted { get; init; } = new Dictionary<string, string>();
    public string LongProc { get; init; } = "org";
}

public sealed class RemoteRuleSourcesJsonConverter : LenientObjectConverter<RemoteRuleSources>
{
    protected override RemoteRuleSources Parse(JsonObject obj, JsonSerializerOptions o) => new()
    {
        Thresholds = Get<Dictionary<string, string>>(obj, "thresholds", o, []),
        Alerts = Get<Dictionary<string, string>>(obj, "alerts", o, []),
        Muted = Get<Dictionary<string, string>>(obj, "muted", o, []),
        LongProc = Get(obj, "longProc", o, "org"),
    };

    public override void Write(Utf8JsonWriter w, RemoteRuleSources v, JsonSerializerOptions o)
    {
        w.WriteStartObject();
        Put(w, "thresholds", v.Thresholds, o);
        Put(w, "alerts", v.Alerts, o);
        Put(w, "muted", v.Muted, o);
        w.WriteString("longProc", v.LongProc);
        w.WriteEndObject();
    }
}

/// <summary>The rules that the engine applies to one host.</summary>
[JsonConverter(typeof(RemoteEffectiveRulesJsonConverter))]
public sealed record RemoteEffectiveRules
{
    public MetricThresholds Thresholds { get; init; } = MetricThresholds.Defaults;
    public AlertRules Alerts { get; init; } = AlertRules.Defaults;
    public MutedSignals Muted { get; init; } = MutedSignals.None;
    public LongProcRule LongProc { get; init; } = LongProcRule.Defaults;
    public IReadOnlyList<string> Ignore { get; init; } = Array.Empty<string>();
    public RemoteRuleSources Sources { get; init; } = new();

    /// <summary>True when the host sets this metric itself.</summary>
    public bool IsHostPair(ThresholdMetric m) => Sources.Thresholds.GetValueOrDefault(m.Key()) == "host";
}

public sealed class RemoteEffectiveRulesJsonConverter : LenientObjectConverter<RemoteEffectiveRules>
{
    protected override RemoteEffectiveRules Parse(JsonObject obj, JsonSerializerOptions o) => new()
    {
        Thresholds = Get(obj, "thresholds", o, MetricThresholds.Defaults),
        Alerts = Get(obj, "alerts", o, AlertRules.Defaults),
        Muted = Get(obj, "muted", o, MutedSignals.None),
        LongProc = Get(obj, "longProc", o, LongProcRule.Defaults),
        Ignore = Get<string[]>(obj, "ignore", o, []),
        Sources = Get(obj, "sources", o, new RemoteRuleSources()),
    };

    public override void Write(Utf8JsonWriter w, RemoteEffectiveRules v, JsonSerializerOptions o)
    {
        w.WriteStartObject();
        Put(w, "thresholds", v.Thresholds, o);
        Put(w, "alerts", v.Alerts, o);
        Put(w, "muted", v.Muted, o);
        Put(w, "longProc", v.LongProc, o);
        Put(w, "ignore", v.Ignore, o);
        Put(w, "sources", v.Sources, o);
        w.WriteEndObject();
    }
}

/// <summary>One host of <see cref="RemoteRulesView"/>.</summary>
public sealed record RemoteRulesHost(
    [property: JsonPropertyName("nodeId")] Guid NodeId,
    [property: JsonPropertyName("host")] RemoteHostRules Host,
    [property: JsonPropertyName("effective")] RemoteEffectiveRules Effective);

/// <summary><c>GET /v1/rules</c>: the org rules and the rules of each host.</summary>
public sealed record RemoteRulesView(
    [property: JsonPropertyName("org")] RemoteOrgRules Org,
    [property: JsonPropertyName("hosts")] IReadOnlyList<RemoteRulesHost> Hosts);

public sealed record RemoteRulesImportHost(
    [property: JsonPropertyName("nodeId")] Guid NodeId,
    [property: JsonPropertyName("rules")] RemoteHostRules Rules);

/// <summary>
/// <c>POST /v1/rules/import</c>. The server replaces the org rules (when <c>Org</c> is set)
/// and the rules of the named hosts at once.
/// </summary>
public sealed record RemoteRulesImport(
    [property: JsonPropertyName("org"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] RemoteOrgRules? Org,
    [property: JsonPropertyName("hosts")] IReadOnlyList<RemoteRulesImportHost> Hosts,
    [property: JsonPropertyName("client")] string Client,
    [property: JsonPropertyName("device")] string Device);

public sealed record RemoteRulesImportResult(
    [property: JsonPropertyName("backupId")] Guid BackupId,
    [property: JsonPropertyName("hosts")] int Hosts,
    [property: JsonPropertyName("org")] bool Org);

/// <summary>Body of <c>PUT /v1/mutes</c>. A null <c>NodeId</c> mutes the metric for the org.</summary>
public sealed record RemoteMuteRequest(
    [property: JsonPropertyName("metric")] string Metric,
    [property: JsonPropertyName("nodeId"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] Guid? NodeId,
    [property: JsonPropertyName("muted")] bool Muted);
