namespace Towertail.WinUI.State;

public sealed record ProcessFacts(
    string? User = null, int? Ppid = null, string? Parent = null, string? Cwd = null, string? Repo = null,
    IReadOnlyList<string>? Ports = null, DateTime? Started = null, DateTime? FactsAt = null, int? Threads = null,
    int? Pid = null, bool Stale = false)
{
    public static readonly ProcessFacts Empty = new();
}

/// <summary>The series and facts of one process. CPU is the percent of one core. RSS is in bytes. I/O is in bytes per second.</summary>
public sealed record ProcessDetail(
    ProcessFacts Facts, IReadOnlyList<TimedValue> Cpu, IReadOnlyList<TimedValue> Rss, IReadOnlyList<TimedValue> Io);

/// <summary>Where the process panel gets its series and facts.</summary>
public interface IProcessDetailSource
{
    Task<ProcessDetail> DetailAsync(ProcessRef r, (DateTime From, DateTime To) window, CancellationToken ct);
}

/// <summary>Builds the detail of a local or SSH host from the process snapshots of the window.</summary>
public sealed class LocalProcessSource(ServerViewModel vm) : IProcessDetailSource
{
    public Task<ProcessDetail> DetailAsync(ProcessRef r, (DateTime From, DateTime To) window, CancellationToken ct)
        => Task.Run(() => Build(r, window, ct), ct);

    private ProcessDetail Build(ProcessRef r, (DateTime From, DateTime To) window, CancellationToken ct)
    {
        var cpu = new List<TimedValue>();
        var rss = new List<TimedValue>();
        var io = new List<TimedValue>();
        (DateTime T, List<ProcSample> Hits)? newest = null;
        foreach (var snap in vm.Procs.In(window).OrderBy(s => s.T))
        {
            ct.ThrowIfCancellationRequested();
            var hits = snap.Items.Where(p => ProcessRef.KeyOf(p) == r.Key).ToList();
            if (hits.Count == 0) continue;
            cpu.Add(new TimedValue(snap.T, hits.Sum(p => p.CpuPct)));
            rss.Add(new TimedValue(snap.T, hits.Sum(p => (double)p.Rss)));
            if (hits.Any(p => p.ReadBps is not null && p.WriteBps is not null))
                io.Add(new TimedValue(snap.T, hits.Sum(p => (double)(Math.Max(p.ReadBps ?? 0, 0) + Math.Max(p.WriteBps ?? 0, 0)))));
            newest = (snap.T, hits);
        }
        return new ProcessDetail(FactsOf(newest, r), cpu, rss, io);
    }

    private ProcessFacts FactsOf((DateTime T, List<ProcSample> Hits)? newest, ProcessRef r)
    {
        if (newest is not { } n) return ProcessFacts.Empty;
        var sample = n.Hits.FirstOrDefault(p => p.Pid == r.Pid) ?? n.Hits[0];
        var ports = new SortedSet<string>(StringComparer.Ordinal);
        foreach (var item in vm.Ports?.Items ?? Array.Empty<PortItem>())
        {
            if (!n.Hits.Any(p => p.Pid == item.Pid)) continue;
            foreach (var port in item.ListenTcp ?? Array.Empty<uint>()) ports.Add($"tcp {port}");
            foreach (var port in item.ListenUdp ?? Array.Empty<uint>()) ports.Add($"udp {port}");
        }
        return new ProcessFacts(sample.User, sample.Ppid, sample.Parent, sample.Cwd, sample.Repo,
            ports.ToList(), sample.StartTs, n.T, sample.Threads);
    }
}
