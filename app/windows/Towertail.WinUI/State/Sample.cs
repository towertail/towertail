using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.State;

/// <summary>
/// Top-level sampler output. Mirrors docs/sampler.md §4 byte-for-byte.
/// Schema is versioned via the top-level <c>v</c> integer — bumps are a wire-contract change.
/// </summary>
public sealed record Sample(
    [property: JsonPropertyName("v")] int V,
    [property: JsonPropertyName("ts")] DateTime Ts,
    [property: JsonPropertyName("host")] HostInfo Host,
    [property: JsonPropertyName("cpu")] CpuInfo Cpu,
    [property: JsonPropertyName("mem")] MemInfo Mem,
    [property: JsonPropertyName("swap")] MemInfo Swap,
    [property: JsonPropertyName("disks")] IReadOnlyList<DiskSample>? Disks,
    [property: JsonPropertyName("disk_io")] DiskIoInfo? DiskIo,
    [property: JsonPropertyName("net")] NetInfo? Net,
    [property: JsonPropertyName("procs")] ProcList? Procs,
    [property: JsonPropertyName("ports")] PortList? Ports,
    [property: JsonPropertyName("errors")] IReadOnlyList<string> Errors
);

public sealed record HostInfo(
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("os")] string Os,
    [property: JsonPropertyName("arch")] string Arch,
    [property: JsonPropertyName("kernel")] string Kernel,
    [property: JsonPropertyName("uptime_s")] long UptimeS,
    [property: JsonPropertyName("sampler")] string Sampler,
    [property: JsonPropertyName("machine_id")] string? MachineId
);

public sealed record CpuInfo(
    [property: JsonPropertyName("pct")] double Pct,
    [property: JsonPropertyName("load_1")] double Load1,
    [property: JsonPropertyName("load_5")] double Load5,
    [property: JsonPropertyName("load_15")] double Load15,
    [property: JsonPropertyName("cores")] int Cores,
    [property: JsonPropertyName("user_ms")] long? UserMs = null,
    [property: JsonPropertyName("system_ms")] long? SystemMs = null,
    [property: JsonPropertyName("idle_ms")] long? IdleMs = null,
    [property: JsonPropertyName("iowait_ms")] long? IowaitMs = null,
    [property: JsonPropertyName("irq_ms")] long? IrqMs = null,
    [property: JsonPropertyName("nice_ms")] long? NiceMs = null,
    [property: JsonPropertyName("steal_ms")] long? StealMs = null,
    [property: JsonPropertyName("total_ms")] long? TotalMs = null
)
{
    /// <summary>Sum of "doing work" time — anything that isn't idle/iowait.</summary>
    public long? BusyMs
    {
        get
        {
            if (TotalMs is null || IdleMs is null) return null;
            var iow = IowaitMs ?? 0;
            return TotalMs.Value - IdleMs.Value - iow;
        }
    }
}

public sealed record MemInfo(
    [property: JsonPropertyName("used")] long Used,
    [property: JsonPropertyName("total")] long Total
);

public sealed record DiskSample(
    [property: JsonPropertyName("mount")] string Mount,
    [property: JsonPropertyName("fs")] string Fs,
    [property: JsonPropertyName("used")] long Used,
    [property: JsonPropertyName("total")] long Total
);

public sealed record NetInfo(
    [property: JsonPropertyName("rx_bps")] long RxBps,
    [property: JsonPropertyName("tx_bps")] long TxBps,
    [property: JsonPropertyName("rx_cum")] long RxCum,
    [property: JsonPropertyName("tx_cum")] long TxCum
);

public sealed record DiskIoInfo(
    [property: JsonPropertyName("read_bps")] long ReadBps,
    [property: JsonPropertyName("write_bps")] long WriteBps,
    [property: JsonPropertyName("read_cum")] long ReadCum,
    [property: JsonPropertyName("write_cum")] long WriteCum,
    [property: JsonPropertyName("devices")] IReadOnlyList<DiskIoDevice>? Devices = null
);

public sealed record DiskIoDevice(
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("read_bps")] long ReadBps,
    [property: JsonPropertyName("write_bps")] long WriteBps,
    [property: JsonPropertyName("read_cum")] long ReadCum,
    [property: JsonPropertyName("write_cum")] long WriteCum
);

public sealed record ProcList(
    [property: JsonPropertyName("root")] bool Root,
    [property: JsonPropertyName("top_n")] int TopN,
    [property: JsonPropertyName("total")] int Total,
    [property: JsonPropertyName("visible")] int Visible,
    [property: JsonPropertyName("items")] IReadOnlyList<ProcSample> Items
);

public sealed record ProcSample(
    [property: JsonPropertyName("pid")] int Pid,
    [property: JsonPropertyName("ppid")] int? Ppid,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("cmd")] string? Cmd,
    [property: JsonPropertyName("user")] string? User,
    [property: JsonPropertyName("cpu_pct")] double CpuPct,
    [property: JsonPropertyName("rss")] long Rss,
    [property: JsonPropertyName("threads")] int? Threads,
    [property: JsonPropertyName("state")] string? State,
    [property: JsonPropertyName("start_ts")] DateTime? StartTs,
    [property: JsonPropertyName("read_bytes")] long? ReadBytes,
    [property: JsonPropertyName("write_bytes")] long? WriteBytes
);

/// <summary>
/// Per-process aggregate of open sockets. Refreshed every
/// <c>--ports-interval</c> (default 10s) on the sampler side and
/// re-emitted unchanged in between — <c>CollectedTs</c> is the wall
/// clock when the snapshot was actually built so the UI can render
/// staleness. <c>Truncated</c> is true when <c>MaxConn</c> was hit.
/// </summary>
public sealed record PortList(
    [property: JsonPropertyName("root")] bool Root,
    [property: JsonPropertyName("collected_ts")] DateTime CollectedTs,
    [property: JsonPropertyName("max_conn")] int MaxConn,
    [property: JsonPropertyName("truncated")] bool Truncated,
    [property: JsonPropertyName("total")] int Total,
    [property: JsonPropertyName("items")] IReadOnlyList<PortItem> Items
);

/// <summary>
/// One process's port footprint. <c>EstOut</c>/<c>EstIn</c> count
/// outbound vs inbound ESTABLISHED TCP connections (inbound = peer
/// connected to one of our listeners). <c>UdpSockets</c> counts UDP
/// sockets without a peer. <c>TopRemotePorts</c> is capped at 5.
/// </summary>
public sealed record PortItem(
    [property: JsonPropertyName("pid")] int Pid,
    [property: JsonPropertyName("name")] string? Name,
    [property: JsonPropertyName("user")] string? User,
    [property: JsonPropertyName("listen_tcp")] IReadOnlyList<uint>? ListenTcp,
    [property: JsonPropertyName("listen_udp")] IReadOnlyList<uint>? ListenUdp,
    [property: JsonPropertyName("est_out")] int EstOut,
    [property: JsonPropertyName("est_in")] int EstIn,
    [property: JsonPropertyName("udp_sockets")] int? UdpSockets,
    [property: JsonPropertyName("top_remote_ports")] IReadOnlyList<PortCount>? TopRemotePorts
);

public sealed record PortCount(
    [property: JsonPropertyName("port")] uint Port,
    [property: JsonPropertyName("count")] int Count
);

/// <summary>
/// Sample JSON codec. Matches the Swift <c>SampleCodec</c> semantics: fractional-second
/// ISO-8601 with optional-millis tolerance, unknown fields ignored.
/// </summary>
public static class SampleCodec
{
    public static JsonSerializerOptions Options { get; } = BuildOptions();

    private static JsonSerializerOptions BuildOptions()
    {
        var o = new JsonSerializerOptions
        {
            PropertyNameCaseInsensitive = false,
            DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
            NumberHandling = JsonNumberHandling.AllowReadingFromString | JsonNumberHandling.AllowNamedFloatingPointLiterals,
        };
        // Use the default DateTime handling — System.Text.Json accepts both
        // fractional-second and no-fraction RFC 3339 out of the box for DateTime.
        return o;
    }

    public static Sample Decode(string json)
    {
        var s = JsonSerializer.Deserialize<Sample>(json, Options)
            ?? throw new JsonException("empty sample");
        if (s.V <= 0) throw new JsonException($"missing or invalid 'v' (got {s.V})");
        if (s.V > 1)
            throw new JsonException($"sample schema v={s.V} is newer than supported v=1");
        return s;
    }

    public static Sample Decode(ReadOnlySpan<byte> utf8)
    {
        var s = JsonSerializer.Deserialize<Sample>(utf8, Options)
            ?? throw new JsonException("empty sample");
        if (s.V <= 0) throw new JsonException($"missing or invalid 'v' (got {s.V})");
        if (s.V > 1)
            throw new JsonException($"sample schema v={s.V} is newer than supported v=1");
        return s;
    }
}
