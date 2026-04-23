namespace Towertail.WinUI.State;

/// <summary>
/// Stores process-table snapshots per timestamp for the FullView PROCS tab scrubber.
/// Only populated once the user opens the full view for a host (see ServerStore.EnsureProcsHydrated).
/// </summary>
public sealed class ProcSeries
{
    public static readonly TimeSpan Retention = TimeSpan.FromHours(2);

    private readonly List<Snapshot> _snapshots = new();
    private readonly object _lock = new();

    public bool Hydrated { get; set; }

    public int Count { get { lock (_lock) return _snapshots.Count; } }

    public void Append(DateTime t, bool root, IReadOnlyList<ProcSample> items)
    {
        lock (_lock)
        {
            _snapshots.Add(new Snapshot(t, root, items));
            TrimLocked();
        }
    }

    public void Replace(IEnumerable<Snapshot> snapshots)
    {
        lock (_lock)
        {
            _snapshots.Clear();
            _snapshots.AddRange(snapshots);
            TrimLocked();
        }
    }

    public Snapshot? Latest
    {
        get
        {
            lock (_lock) return _snapshots.Count == 0 ? null : _snapshots[^1];
        }
    }

    public Snapshot? At(DateTime t)
    {
        lock (_lock)
        {
            if (_snapshots.Count == 0) return null;
            // Find the snapshot closest to (but not after) t.
            Snapshot? best = null;
            foreach (var s in _snapshots)
            {
                if (s.T <= t) best = s;
                else break;
            }
            return best ?? _snapshots[0];
        }
    }

    private void TrimLocked()
    {
        var cutoff = DateTime.UtcNow - Retention;
        int keep = 0;
        while (keep < _snapshots.Count && _snapshots[keep].T < cutoff) keep++;
        if (keep > 0) _snapshots.RemoveRange(0, keep);
    }

    public sealed record Snapshot(DateTime T, bool Root, IReadOnlyList<ProcSample> Items);
}
