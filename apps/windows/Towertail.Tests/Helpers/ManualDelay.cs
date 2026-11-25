namespace Towertail.Tests.Helpers;

/// <summary>An injected delay that completes when the test says so. No test waits in real time.</summary>
public sealed class ManualDelay
{
    private readonly List<(TimeSpan Span, TaskCompletionSource Tcs)> _pending = new();

    public Func<TimeSpan, CancellationToken, Task> Func => DelayAsync;

    public IReadOnlyList<TimeSpan> Pending
    {
        get { lock (_pending) return _pending.Select(p => p.Span).ToList(); }
    }

    private Task DelayAsync(TimeSpan span, CancellationToken ct)
    {
        var tcs = new TaskCompletionSource();
        lock (_pending) _pending.Add((span, tcs));
        ct.Register(() =>
        {
            lock (_pending) _pending.RemoveAll(p => p.Tcs == tcs);
            tcs.TrySetCanceled(ct);
        });
        return tcs.Task;
    }

    /// <summary>Completes the oldest pending delay of this length. Continuations run inline.</summary>
    public bool Release(TimeSpan span)
    {
        TaskCompletionSource? tcs = null;
        lock (_pending)
        {
            var i = _pending.FindIndex(p => p.Span == span);
            if (i < 0) return false;
            tcs = _pending[i].Tcs;
            _pending.RemoveAt(i);
        }
        tcs.TrySetResult();
        return true;
    }
}
