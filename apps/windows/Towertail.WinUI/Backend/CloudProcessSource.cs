using Towertail.WinUI.State;

namespace Towertail.WinUI.Backend;

/// <summary>Builds the detail of a cloud process from the one-process route.</summary>
public sealed class CloudProcessSource(Guid host, CloudHistoryCache cache, TimeProvider? time = null) : IProcessDetailSource
{
    /// <summary>The server keeps the facts for 1 h.</summary>
    public static readonly TimeSpan FactsAge = TimeSpan.FromHours(1);

    private readonly TimeProvider _time = time ?? TimeProvider.System;

    public async Task<ProcessDetail> DetailAsync(ProcessRef r, (DateTime From, DateTime To) window, CancellationToken ct)
    {
        var span = (window.To - window.From).TotalSeconds;
        var step = TimeSpan.FromSeconds(Math.Max(10, Math.Ceiling(span / 300)));
        var d = await cache.ProcessAsync(host, r.Key, window.From, window.To, step, ct).ConfigureAwait(false);
        return new ProcessDetail(
            Facts(d),
            d.Points.Select(p => new TimedValue(p.Ts, p.CpuPct)).ToList(),
            d.Points.Select(p => new TimedValue(p.Ts, p.Rss)).ToList(),
            d.Points.Where(p => p.IoBps is not null).Select(p => new TimedValue(p.Ts, p.IoBps!.Value)).ToList());
    }

    public ProcessFacts Facts(RemoteProcessDetail d)
    {
        if (d.Facts is not { } f) return new ProcessFacts(Repo: d.Repo, Stale: true);
        var ports = (f.ListenTcp ?? new()).Select(p => $"tcp {p}").Concat((f.ListenUdp ?? new()).Select(p => $"udp {p}")).ToList();
        var stale = _time.GetUtcNow().UtcDateTime - f.SeenAt > FactsAge;
        return new ProcessFacts(f.User, f.Ppid, f.Parent, f.Cwd, d.Repo, ports, f.StartTs, f.SeenAt, f.Threads, f.Pid, stale);
    }
}
