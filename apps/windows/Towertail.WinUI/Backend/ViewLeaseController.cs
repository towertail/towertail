namespace Towertail.WinUI.Backend;

/// <summary>
/// Tells the cloud which hosts the user looks at. It builds the <c>view</c> frame from the
/// visible popover cards, the watched hosts and the front full view, sends it at each change,
/// and renews it while the view is not empty. A hidden popover or a sleeping system gives an
/// empty view at once. Mirrors the Mac <c>ViewLeaseController</c>.
/// </summary>
public sealed class ViewLeaseController(Func<LiveView, Task> send, Func<TimeSpan, CancellationToken, Task>? delay = null)
{
    public static readonly TimeSpan RenewEvery = TimeSpan.FromSeconds(30);
    public static readonly TimeSpan CardDebounce = TimeSpan.FromMilliseconds(250);
    public const int MaxHosts = 500;

    private sealed record FullViewEntry(object Window, Guid Host, DetailTab? Tab);

    private readonly Func<TimeSpan, CancellationToken, Task> _delay = delay ?? Task.Delay;
    private readonly object _gate = new();
    private HashSet<Guid> _cards = [];
    private readonly HashSet<Guid> _watched = [];
    private readonly List<FullViewEntry> _fullViews = [];
    private bool _popoverVisible;
    private bool _awake = true;
    private bool _limited;
    private LiveView _current = LiveView.Empty;
    private CancellationTokenSource? _renew;
    private CancellationTokenSource? _debounce;

    /// <summary>The view that the controller sent last.</summary>
    public LiveView Current { get { lock (_gate) return _current; } }

    /// <summary>The popover shows or hides. <paramref name="cardHosts"/> are the cards that are on screen.</summary>
    public void SetPopover(bool visible, IReadOnlyList<Guid> cardHosts)
    {
        lock (_gate)
        {
            var cards = visible ? cardHosts.ToHashSet() : [];
            if (_popoverVisible == visible && _cards.SetEquals(cards)) return;
            _popoverVisible = visible;
            _cards = cards;
            Refresh(debounced: false);
        }
    }

    /// <summary>A card scrolls in or out of the popover. Changes within the debounce time give one view.</summary>
    public void SetCard(Guid host, bool visible)
    {
        lock (_gate)
        {
            var changed = visible ? _cards.Add(host) : _cards.Remove(host);
            if (changed) Refresh(debounced: true);
        }
    }

    /// <summary>A task needs live data for a host, for example an install that waits for the first sample.</summary>
    public void SetWatched(Guid host, bool watched)
    {
        lock (_gate)
        {
            var changed = watched ? _watched.Add(host) : _watched.Remove(host);
            if (changed) Refresh(debounced: false);
        }
    }

    public void SetFullView(object window, Guid host, DetailTab? tab, bool visible)
    {
        lock (_gate)
        {
            var old = _fullViews.Find(e => ReferenceEquals(e.Window, window));
            if (!visible && old is null) return;
            if (visible && old is not null && old.Host == host && old.Tab == tab) return;
            _fullViews.RemoveAll(e => ReferenceEquals(e.Window, window));
            if (visible) _fullViews.Add(new FullViewEntry(window, host, tab));
            Refresh(debounced: false);
        }
    }

    public void SetSystemAwake(bool awake)
    {
        lock (_gate)
        {
            if (_awake == awake) return;
            _awake = awake;
            Refresh(debounced: false);
        }
    }

    /// <summary>A new connection or a restarted server has no lease: send the view again.</summary>
    public void StreamDidConnect()
    {
        lock (_gate)
        {
            if (_current.Equals(LiveView.Empty)) return;
            Emit(_current);
        }
    }

    /// <summary>The server ended the lease because of its limit. Do not renew until the next change of the input.</summary>
    public void DidReceiveLimit()
    {
        lock (_gate)
        {
            _limited = true;
            CancelRenew();
        }
    }

    /// <summary>Stops the timers. The next change sends the view again.</summary>
    public void Stop()
    {
        lock (_gate)
        {
            CancelRenew();
            _debounce?.Cancel();
            _debounce = null;
            _current = LiveView.Empty;
        }
    }

    private LiveView Desired()
    {
        if (!_awake) return LiveView.Empty;
        var hosts = new HashSet<Guid>(_watched);
        if (_popoverVisible) hosts.UnionWith(_cards);
        foreach (var e in _fullViews) hosts.Add(e.Host);
        if (hosts.Count == 0) return LiveView.Empty;
        ViewDetail? detail = null;
        if (_fullViews.Count > 0 && _fullViews[^1] is { Tab: { } tab } last)
            detail = new ViewDetail(last.Host, tab);
        var sorted = hosts.OrderBy(h => h.ToString(), StringComparer.Ordinal).Take(MaxHosts).ToList();
        return new LiveView(sorted, detail);
    }

    private void Refresh(bool debounced)
    {
        _debounce?.Cancel();
        _debounce = null;
        if (!debounced) { Flush(); return; }
        var cts = _debounce = new CancellationTokenSource();
        _ = DebounceAsync(cts);
    }

    private async Task DebounceAsync(CancellationTokenSource cts)
    {
        try { await _delay(CardDebounce, cts.Token).ConfigureAwait(false); }
        catch (OperationCanceledException) { return; }
        lock (_gate)
        {
            if (cts.IsCancellationRequested) return;
            _debounce = null;
            Flush();
        }
    }

    private void Flush()
    {
        var next = Desired();
        var changed = !next.Equals(_current);
        var wasLimited = _limited;
        _limited = false;
        _current = next;
        if (next.Equals(LiveView.Empty))
        {
            CancelRenew();
            if (changed) Send(next);
        }
        else if (changed || wasLimited)
        {
            Emit(next);
        }
    }

    /// <summary>Sends a non-empty view and starts the renew timer.</summary>
    private void Emit(LiveView view)
    {
        Send(view);
        CancelRenew();
        var cts = _renew = new CancellationTokenSource();
        _ = RenewAsync(cts.Token);
    }

    private async Task RenewAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try { await _delay(RenewEvery, ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
            lock (_gate)
            {
                if (ct.IsCancellationRequested) return;
                Send(_current);
            }
        }
    }

    private void CancelRenew()
    {
        _renew?.Cancel();
        _renew = null;
    }

    // RemoteStream logs its own send errors, so a failed send is dropped here.
    private void Send(LiveView view)
    {
        try { _ = send(view).ContinueWith(t => _ = t.Exception, TaskContinuationOptions.OnlyOnFaulted); }
        catch { }
    }
}
