using System.Runtime.CompilerServices;
using System.Threading.Channels;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>The <c>disk_scan_updated</c> frames of the server stream.</summary>
public interface IDiskScanUpdates
{
    event Action<Guid, DiskScanJob>? DiskScanUpdated;
}

/// <summary>
/// Scans through the server (remote mode): starts a job, then turns the
/// <c>disk_scan_updated</c> frames into events. It also polls the job, so that a lost
/// WebSocket does not stop the scan. When the job is done, it downloads the result.
/// Cancel sends <c>POST …/cancel</c> and waits for the final state.
/// </summary>
public sealed class RemoteDiskScanner : IDiskScanner
{
    public const string OptInOffMessage =
        "Disk scans are off for this node. Turn on \"Allow disk scans\" for the node on the server, " +
        "and run the sampler with --allow-disk-scan.";

    private readonly RemoteClient _client;
    private readonly IDiskScanUpdates? _updates;
    private readonly TimeSpan _poll;
    private readonly TimeSpan _cancelGrace;

    public RemoteDiskScanner(RemoteClient client, IDiskScanUpdates? updates,
        TimeSpan? pollInterval = null, TimeSpan? cancelGrace = null)
    {
        _client = client;
        _updates = updates;
        _poll = pollInterval ?? TimeSpan.FromSeconds(5);
        _cancelGrace = cancelGrace ?? TimeSpan.FromSeconds(15);
    }

    public async IAsyncEnumerable<DiskScanEvent> ScanAsync(
        Node node, DiskScanRequest request, [EnumeratorCancellation] CancellationToken ct = default)
    {
        DiskScanJob? job = null;
        DiskScanEvent? failure = null;
        try { job = await _client.StartDiskScanAsync(node.Id, request, ct).ConfigureAwait(false); }
        catch (RemoteException ex) when (ex.Status == 403) { failure = new DiskScanEvent.Error(OptInOffMessage); }
        catch (OperationCanceledException) { failure = new DiskScanEvent.Cancelled(); }
        catch (Exception ex) { failure = new DiskScanEvent.Error($"Could not start the scan: {ex.Message}"); }
        if (job is null)
        {
            yield return failure ?? new DiskScanEvent.Error("Could not start the scan.");
            yield break;
        }
        await foreach (var ev in FollowAsync(job, ct).ConfigureAwait(false))
            yield return ev;
    }

    /// <summary>The newest job of the node, or null. Use it to show a scan that started on another device.</summary>
    public async Task<DiskScanJob?> LatestJobAsync(Guid nodeId, CancellationToken ct = default)
    {
        try { return (await _client.ListDiskScansAsync(nodeId, ct).ConfigureAwait(false)).FirstOrDefault(); }
        catch { return null; }
    }

    /// <summary>Follow a job until it is in a final state.</summary>
    public async IAsyncEnumerable<DiskScanEvent> FollowAsync(
        DiskScanJob job, [EnumeratorCancellation] CancellationToken ct = default)
    {
        var inbox = Channel.CreateUnbounded<DiskScanJob>();
        Action<Guid, DiskScanJob> handler = (_, j) => { if (j.Id == job.Id) inbox.Writer.TryWrite(j); };
        if (_updates != null) _updates.DiskScanUpdated += handler;
        try
        {
            var current = job;
            DiskScanProgress? lastProgress = null;
            DateTime? cancelSentAt = null;
            yield return new DiskScanEvent.Job(current);

            while (true)
            {
                if (current.Progress is { } p && p != lastProgress)
                {
                    lastProgress = p;
                    yield return new DiskScanEvent.Progress(p);
                }
                if (current.IsFinal)
                {
                    yield return await FinalEventAsync(current).ConfigureAwait(false);
                    yield break;
                }
                if (ct.IsCancellationRequested && cancelSentAt is null)
                {
                    cancelSentAt = DateTime.UtcNow;
                    if (await TryCancelAsync(current.Id).ConfigureAwait(false) is { } after && after != current)
                    {
                        current = after;
                        yield return new DiskScanEvent.Job(current);
                    }
                    continue;
                }
                if (cancelSentAt is DateTime sent && DateTime.UtcNow - sent > _cancelGrace)
                {
                    yield return new DiskScanEvent.Cancelled();
                    yield break;
                }

                var next = await NextAsync(inbox.Reader, current, cancelSentAt is null ? ct : CancellationToken.None)
                    .ConfigureAwait(false);
                if (next != current)
                {
                    current = next;
                    yield return new DiskScanEvent.Job(current);
                }
            }
        }
        finally
        {
            if (_updates != null) _updates.DiskScanUpdated -= handler;
        }
    }

    // Waits for a frame, the poll time or the cancel. Without a frame it fetches the job.
    private async Task<DiskScanJob> NextAsync(ChannelReader<DiskScanJob> inbox, DiskScanJob current, CancellationToken ct)
    {
        if (inbox.TryRead(out var queued)) return Newest(inbox, queued);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(ct);
        linked.CancelAfter(_poll);
        try
        {
            var job = await inbox.ReadAsync(linked.Token).ConfigureAwait(false);
            return Newest(inbox, job);
        }
        catch (OperationCanceledException) when (ct.IsCancellationRequested) { return current; }
        catch (OperationCanceledException) { }
        try { return await _client.GetDiskScanAsync(current.Id).ConfigureAwait(false); }
        catch { return current; }
    }

    private static DiskScanJob Newest(ChannelReader<DiskScanJob> inbox, DiskScanJob job)
    {
        while (inbox.TryRead(out var more)) job = more;
        return job;
    }

    private async Task<DiskScanJob?> TryCancelAsync(Guid jobId)
    {
        try { return await _client.CancelDiskScanAsync(jobId).ConfigureAwait(false); }
        catch { return null; }
    }

    private async Task<DiskScanEvent> FinalEventAsync(DiskScanJob job)
    {
        switch (job.State)
        {
            case DiskScanJob.Done:
                try { return new DiskScanEvent.Result(await _client.GetDiskScanResultAsync(job.Id).ConfigureAwait(false)); }
                catch (Exception ex) { return new DiskScanEvent.Error($"Could not download the result: {ex.Message}"); }
            case DiskScanJob.CancelledState:
                return new DiskScanEvent.Cancelled();
            case DiskScanJob.Expired:
                return new DiskScanEvent.Error(string.IsNullOrEmpty(job.Error)
                    ? "No sampler picked up the scan in 10 min." : job.Error);
            default:
                return new DiskScanEvent.Error(string.IsNullOrEmpty(job.Error) ? "The scan failed." : job.Error);
        }
    }
}
