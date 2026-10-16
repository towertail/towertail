using System.Globalization;
using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.Collectors;

namespace Towertail.WinUI.State;

public enum DiskUsagePhase { Idle, Running, Done, Failed, Cancelled }

/// <summary>The state of a map tile in the cleanup list.</summary>
public abstract record TileMark
{
    public sealed record None : TileMark;
    /// <summary>A recommendation points to the tile.</summary>
    public sealed record Rule(DiskRec Rec, bool On) : TileMark;
    /// <summary>The user added the tile.</summary>
    public sealed record Mine(bool On) : TileMark;
    /// <summary>A folder above the tile is in the list and checked.</summary>
    public sealed record Covered(string By) : TileMark;
}

/// <summary>A folder or file that the user added, with its node in the tree.</summary>
public sealed record MineItem(string Path, string DisplayPath, IReadOnlyList<string> Names, DiskNode Node, bool On)
{
    public string Label
    {
        get
        {
            var last = Node.N.Split('/', '\\').Last();
            return Node.F ? last : last + "/";
        }
    }
}

/// <summary>One step of the remote job strip: Queued → Sampler picked up → Scanned → Result in.</summary>
public sealed record DiskJobStep(string Label, string Detail, bool Reached);

/// <summary>
/// State of the Usage sub-tab for one node: the scan phase, the progress, the result and
/// the selected recommendations. Free of WinUI types. Property changes go through the
/// injected marshaller (<see cref="RealCollector.UiMarshaller"/>), so that the view can bind.
/// </summary>
public sealed partial class DiskUsageModel : ObservableObject
{
    public static readonly TimeSpan StaleAfter = TimeSpan.FromHours(24);
    public const string HomeScope = "~";

    private readonly Node _node;
    private readonly IDiskScanner _scanner;
    private readonly DiskScanStore? _store;
    private readonly Func<Action, Task>? _marshal;
    private readonly Func<DateTime> _now;
    private readonly HashSet<string> _selected = new();
    private readonly List<CleanupPick> _picks = new();
    private CancellationTokenSource? _cts;
    private CancellationTokenSource? _refreshCts;

    [ObservableProperty] private DiskUsagePhase _phase = DiskUsagePhase.Idle;
    [ObservableProperty] private DiskScanProgress? _progress;
    /// <summary>The last result. While a new scan runs, this is the previous one.</summary>
    [ObservableProperty] private DiskScanResult? _result;
    [ObservableProperty] private DiskScanJob? _job;
    [ObservableProperty] private string? _error;
    [ObservableProperty] private string _scope;
    [ObservableProperty] private DateTime? _scanStartedAt;
    /// <summary>A short message for the toast.</summary>
    [ObservableProperty] private string? _notice;
    /// <summary>Names of the folder that a refresh scans now.</summary>
    [ObservableProperty] private IReadOnlyList<string>? _refreshing;

    public DiskUsageModel(Node node, IDiskScanner scanner, DiskScanStore? store = null,
        Func<Action, Task>? marshal = null, Func<DateTime>? now = null, string? scope = null)
    {
        _node = node;
        _scanner = scanner;
        _store = store;
        _marshal = marshal;
        _now = now ?? (() => DateTime.UtcNow);
        _scope = scope ?? HomeScope;
    }

    /// <summary>The first scope: the home folder, but the whole disk on Linux.</summary>
    public static string DefaultScope(string? hostOs) => hostOs == "linux" ? "/" : HomeScope;

    /// <summary>The scope picker items: the home folder, then each mount.</summary>
    public static IReadOnlyList<string> Scopes(IEnumerable<string> mounts)
        => new[] { HomeScope }.Concat(mounts.Where(m => !string.IsNullOrEmpty(m))).Distinct().ToList();

    public Node Node => _node;
    public bool IsRemote => _scanner is RemoteDiskScanner;
    public IReadOnlySet<string> Selected => _selected;
    /// <summary>The cleanup picks of the scope, the oldest first.</summary>
    public IReadOnlyList<CleanupPick> Picks => _picks;
    /// <summary>A scan or a refresh runs.</summary>
    public bool IsBusy => Phase == DiskUsagePhase.Running || Refreshing != null;
    /// <summary>The selection or the picks changed.</summary>
    public event EventHandler? SelectionChanged;

    /// <summary>Show the newest kept result of the current scope, and its picks.</summary>
    public void LoadLatest()
    {
        var entry = _store?.Latest(_node.Id, Scope);
        if (Phase != DiskUsagePhase.Running)
        {
            Result = entry?.Result;
            Phase = entry is null ? DiskUsagePhase.Idle : DiskUsagePhase.Done;
        }
        LoadPicks();
        SelectSafe();
    }

    private void LoadPicks()
    {
        _picks.Clear();
        if (_store != null) _picks.AddRange(_store.Picks(_node.Id, Scope));
        PrunePicks();
    }

    public Task StartAsync()
    {
        if (Refreshing != null) return Task.CompletedTask;
        var request = new DiskScanRequest(Root: Scope);
        return RunAsync(ct => _scanner.ScanAsync(_node, request, ct));
    }

    /// <summary>Follow events from another source, for example a server job that runs now.</summary>
    public async Task RunAsync(Func<CancellationToken, IAsyncEnumerable<DiskScanEvent>> source)
    {
        if (Phase == DiskUsagePhase.Running) return;
        var cts = new CancellationTokenSource();
        _cts = cts;
        var scope = Scope;
        await OnUi(() =>
        {
            Phase = DiskUsagePhase.Running;
            Progress = null;
            Job = null;
            Error = null;
            ScanStartedAt = _now();
        }).ConfigureAwait(false);

        try
        {
            await foreach (var ev in source(cts.Token).ConfigureAwait(false))
                await ApplyAsync(ev, scope).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            await OnUi(() => { Phase = DiskUsagePhase.Failed; Error = ex.Message; }).ConfigureAwait(false);
        }
        finally
        {
            await OnUi(() =>
            {
                if (Phase == DiskUsagePhase.Running)
                {
                    Phase = DiskUsagePhase.Failed;
                    Error ??= "The scan ended without a result.";
                }
            }).ConfigureAwait(false);
            if (ReferenceEquals(_cts, cts)) _cts = null;
            cts.Dispose();
        }
    }

    public void Cancel()
    {
        try { _cts?.Cancel(); } catch (ObjectDisposedException) { }
    }

    private async Task ApplyAsync(DiskScanEvent ev, string scope)
    {
        switch (ev)
        {
            case DiskScanEvent.Progress p:
                await OnUi(() => Progress = p.Line).ConfigureAwait(false);
                break;
            case DiskScanEvent.Job j:
                await OnUi(() => Job = j.Value).ConfigureAwait(false);
                break;
            case DiskScanEvent.Result r:
                try { _store?.Save(_node.Id, scope, r.Value); } catch { /* the result still shows */ }
                await OnUi(() =>
                {
                    Result = r.Value;
                    Phase = DiskUsagePhase.Done;
                    if (scope == Scope) LoadPicks();
                    SelectSafe();
                }).ConfigureAwait(false);
                break;
            case DiskScanEvent.Error e:
                await OnUi(() => { Phase = DiskUsagePhase.Failed; Error = e.Message; }).ConfigureAwait(false);
                break;
            case DiskScanEvent.Cancelled:
                await OnUi(() => Phase = DiskUsagePhase.Cancelled).ConfigureAwait(false);
                break;
        }
    }

    private Task OnUi(Action a)
    {
        if (_marshal is null) { a(); return Task.CompletedTask; }
        return _marshal(a);
    }

    // Refresh

    /// <summary>Scans the folder at <paramref name="names"/> again and merges the result. A file refreshes its folder. The root starts a full scan.</summary>
    public Task RefreshAsync(IReadOnlyList<string> names)
    {
        if (IsBusy || Result is not { } result) return Task.CompletedTask;
        var list = names.ToList();
        if (list.Count > 0 && result.Tree.NodeAt(list)?.F == true) list.RemoveAt(list.Count - 1);
        if (list.Count == 0) return StartAsync();
        var request = result.RefreshRequest(list) with { RefreshOf = Scope };
        Notice = null;
        return RunRefreshAsync(ct => _scanner.ScanAsync(_node, request, ct), list);
    }

    private async Task RunRefreshAsync(Func<CancellationToken, IAsyncEnumerable<DiskScanEvent>> source, IReadOnlyList<string> names)
    {
        var cts = new CancellationTokenSource();
        _refreshCts = cts;
        var scope = Scope;
        await OnUi(() => Refreshing = names).ConfigureAwait(false);
        var full = false;
        DiskScanResult? sub = null;
        string? failure = "The refresh stopped without a result.";
        try
        {
            await foreach (var ev in source(cts.Token).ConfigureAwait(false))
            {
                if (full) { await ApplyAsync(ev, scope).ConfigureAwait(false); continue; }
                if (!ReferenceEquals(_refreshCts, cts)) return;
                switch (ev)
                {
                    case DiskScanEvent.Job { Value: var j } when !j.IsRefresh:
                        // The server runs a full scan already. Show it.
                        full = true;
                        _refreshCts = null;
                        _cts = cts;
                        await OnUi(() =>
                        {
                            Refreshing = null;
                            Job = j;
                            Error = null;
                            Progress = j.Progress;
                            ScanStartedAt = j.CreatedTs ?? _now();
                            Phase = DiskUsagePhase.Running;
                        }).ConfigureAwait(false);
                        break;
                    case DiskScanEvent.Job j:
                        await OnUi(() => Job = j.Value).ConfigureAwait(false);
                        break;
                    case DiskScanEvent.Result r:
                        sub = r.Value;
                        failure = null;
                        break;
                    case DiskScanEvent.Error e:
                        failure = e.Message;
                        break;
                    case DiskScanEvent.Cancelled:
                        failure = null;
                        break;
                }
                if (ev.IsFinal) break;
            }
        }
        catch (Exception ex)
        {
            if (full) await OnUi(() => { Phase = DiskUsagePhase.Failed; Error = ex.Message; }).ConfigureAwait(false);
            else failure = ex.Message;
        }
        finally
        {
            if (full)
            {
                await OnUi(() =>
                {
                    if (Phase != DiskUsagePhase.Running) return;
                    Phase = DiskUsagePhase.Failed;
                    Error ??= "The scan ended without a result.";
                }).ConfigureAwait(false);
                if (ReferenceEquals(_cts, cts)) _cts = null;
            }
            cts.Dispose();
        }
        if (full || !ReferenceEquals(_refreshCts, cts)) return;
        _refreshCts = null;
        await OnUi(() =>
        {
            Refreshing = null;
            if (sub != null) EndRefresh(sub, names, scope);
            else if (failure != null) Notice = $"The refresh failed: {failure.TrimEnd('.')}. The map shows the previous result.";
        }).ConfigureAwait(false);
    }

    /// <summary>Stops a refresh. The shown result stays.</summary>
    public void CancelRefresh()
    {
        if (_refreshCts is not { } cts) return;
        _refreshCts = null;
        try { cts.Cancel(); } catch (ObjectDisposedException) { }
        Refreshing = null;
    }

    private void EndRefresh(DiskScanResult sub, IReadOnlyList<string> names, string scope)
    {
        if (scope != Scope || Result is not { } old) return;
        if (old.Merging(sub, names, _now()) is not { } merged)
        {
            Notice = "The folder is no longer in the scan.";
            return;
        }
        var before = old.Tree.NodeAt(names)?.B ?? 0;
        var oldIds = old.Recommendations.Select(r => r.Id).ToHashSet();
        try { _store?.ReplaceLatest(_node.Id, scope, merged); } catch { /* the merged result still shows */ }
        Result = merged;
        var ids = merged.Recommendations.Select(r => r.Id).ToHashSet();
        _selected.IntersectWith(ids);
        foreach (var r in VisibleRecs)
            if (r.IsSafe && !oldIds.Contains(r.Id)) _selected.Add(r.Id);

        var name = names[^1].Split('/', '\\').Last();
        var diff = before - sub.Tree.B;
        var text = $"{name} refreshed. " + (diff > 0 ? $"{DiskSize.Format(diff)} less than before."
            : diff < 0 ? $"{DiskSize.Format(-diff)} more than before." : "No change in size.");
        Notice = null;
        PrunePicks();
        Notice = Notice is { } gone ? text + " " + gone : text;
        Changed();
    }

    // Selection

    /// <summary>The recommendations that the user did not hide.</summary>
    public IReadOnlyList<DiskRec> VisibleRecs => Result?.Recommendations.Where(r => !IsHidden(r)).ToList() ?? new List<DiskRec>();

    public IReadOnlyList<DiskRec> HiddenRecs => Result?.Recommendations.Where(IsHidden).ToList() ?? new List<DiskRec>();

    public bool IsHidden(DiskRec r)
    {
        var key = CleanupPick.HiddenKey(r);
        return _picks.Any(p => p.Kind == CleanupPickKind.Hidden && p.Key == key);
    }

    public IReadOnlyList<DiskRec> SelectedRecs => VisibleRecs.Where(r => _selected.Contains(r.Id)).ToList();

    /// <summary>The items that the user added, with their nodes in the tree.</summary>
    public IReadOnlyList<MineItem> MineItems
    {
        get
        {
            if (Result is not { } result) return Array.Empty<MineItem>();
            var list = new List<MineItem>();
            foreach (var p in _picks)
            {
                if (p.Kind != CleanupPickKind.Mine || result.NamesOf(p.Key) is not { } names
                    || result.Tree.NodeAt(names) is not { } n) continue;
                list.Add(new MineItem(p.Key, result.DisplayPath(p.Key), names, n, p.On));
            }
            return list;
        }
    }

    public IReadOnlyList<MineItem> SelectedMine => MineItems.Where(i => i.On).ToList();

    public int SelectedCount => SelectedRecs.Count + SelectedMine.Count;

    public void Toggle(string recId)
    {
        if (!_selected.Remove(recId)) _selected.Add(recId);
        Changed();
    }

    public void SelectSafe()
    {
        _selected.Clear();
        foreach (var r in VisibleRecs)
            if (r.IsSafe) _selected.Add(r.Id);
        Changed();
    }

    /// <summary>Clears the boxes. The items of the user stay in the list, not checked.</summary>
    public void ClearSelection()
    {
        _selected.Clear();
        foreach (var p in _picks.Where(p => p.Kind == CleanupPickKind.Mine && p.On).ToList())
            SavePick(p with { On = false });
        Changed();
    }

    public long SelectedBytes => SelectedRecs.Sum(r => r.Bytes) + SelectedMine.Sum(i => i.Node.B);

    /// <summary>Tree node ids that the selected recommendations cover.</summary>
    public IReadOnlySet<string> SelectedNodeIds
        => SelectedRecs.SelectMany(r => r.NodeIds ?? Array.Empty<string>()).ToHashSet();

    public CleanupScript? Script => Result is { } r
        ? new CleanupScript(r, SelectedRecs.Select(x => x.Id).ToHashSet(), _now(), SelectedMine.Select(i => i.Path))
        : null;

    // Cleanup list

    public TileMark Mark(IReadOnlyList<string> names)
    {
        if (Result is not { } result || names.Count == 0) return new TileMark.None();
        if (CoveredBy(names) is { } by) return new TileMark.Covered(by);
        if (RecAt(names) is { } r && !IsHidden(r)) return new TileMark.Rule(r, _selected.Contains(r.Id));
        var path = result.AbsolutePath(names);
        if (_picks.FirstOrDefault(p => p.Kind == CleanupPickKind.Mine && p.Key == path) is { } pick)
            return new TileMark.Mine(pick.On);
        return new TileMark.None();
    }

    /// <summary>True when a checked item is at or below the tile.</summary>
    public bool HoldsSelection(IReadOnlyList<string> names)
    {
        if (Result is not { } result || result.Tree.NodeAt(names) is not { } n) return false;
        if (n.SubtreeIds.Overlaps(SelectedNodeIds)) return true;
        var path = result.AbsolutePath(names);
        return SelectedMine.Any(i => DiskScanResult.IsIn(i.Path, path, result.Separator));
    }

    /// <summary>The name of the checked folder above <paramref name="names"/>, if there is one.</summary>
    public string? CoveredBy(IReadOnlyList<string> names)
    {
        if (Result is not { } result || names.Count < 2) return null;
        var ids = SelectedNodeIds;
        var on = SelectedMine.Select(i => i.Path).ToHashSet();
        for (var k = names.Count - 1; k >= 1; k--)
        {
            var up = names.Take(k).ToList();
            if (result.Tree.NodeAt(up)?.Id is { } id && ids.Contains(id)) return up[^1];
            if (on.Contains(result.AbsolutePath(up))) return up[^1];
        }
        return null;
    }

    /// <summary>Puts the tile in the list, checked. A recommendation of the tile is shown again and selected. Items of the user below it leave the list.</summary>
    public void Add(IReadOnlyList<string> names)
    {
        if (Result is not { } result || names.Count == 0 || CoveredBy(names) != null) return;
        if (RecAt(names) is { } r)
        {
            if (IsHidden(r)) Unhide(r);
            _selected.Add(r.Id);
            Changed();
            return;
        }
        var path = result.AbsolutePath(names);
        foreach (var p in _picks.Where(p => p.Kind == CleanupPickKind.Mine && p.Key != path
                     && DiskScanResult.IsIn(p.Key, path, result.Separator)).ToList())
            RemoveMine(p.Key);
        SetMine(path, true);
    }

    /// <summary>Takes the tile out of the list. A recommendation becomes hidden.</summary>
    public void Remove(IReadOnlyList<string> names)
    {
        switch (Mark(names))
        {
            case TileMark.Rule r: Hide(r.Rec); break;
            case TileMark.Mine: RemoveMine(Result!.AbsolutePath(names)); break;
        }
    }

    /// <summary>The box on a tile: adds the tile, or checks or clears it.</summary>
    public void ToggleTile(IReadOnlyList<string> names)
    {
        switch (Mark(names))
        {
            case TileMark.None: Add(names); break;
            case TileMark.Rule r: Toggle(r.Rec.Id); break;
            case TileMark.Mine m: SetMine(Result!.AbsolutePath(names), !m.On); break;
        }
    }

    public void SetMine(string path, bool on)
    {
        var p = _picks.FirstOrDefault(p => p.Kind == CleanupPickKind.Mine && p.Key == path)
                ?? new CleanupPick(CleanupPickKind.Mine, path, _now());
        SavePick(p with { On = on });
        Changed();
    }

    public void RemoveMine(string path)
    {
        try { _store?.RemovePick(_node.Id, Scope, CleanupPickKind.Mine, path); } catch { }
        _picks.RemoveAll(p => p.Kind == CleanupPickKind.Mine && p.Key == path);
        Changed();
    }

    public void Hide(DiskRec r)
    {
        SavePick(new CleanupPick(CleanupPickKind.Hidden, CleanupPick.HiddenKey(r), _now()));
        _selected.Remove(r.Id);
        Changed();
    }

    public void Unhide(DiskRec r)
    {
        var key = CleanupPick.HiddenKey(r);
        try { _store?.RemovePick(_node.Id, Scope, CleanupPickKind.Hidden, key); } catch { }
        _picks.RemoveAll(p => p.Kind == CleanupPickKind.Hidden && p.Key == key);
        Changed();
    }

    private void SavePick(CleanupPick p)
    {
        try { _store?.SavePick(_node.Id, Scope, p); } catch { /* the list still changes */ }
        var i = _picks.FindIndex(x => x.Kind == p.Kind && x.Key == p.Key);
        if (i >= 0) _picks[i] = p; else _picks.Add(p);
    }

    /// <summary>The recommendation that points to the node at <paramref name="names"/>.</summary>
    private DiskRec? RecAt(IReadOnlyList<string> names)
    {
        if (Result?.Tree.NodeAt(names)?.Id is not { } id) return null;
        return Result.Recommendations.FirstOrDefault(r => r.NodeIds?.Contains(id) == true);
    }

    /// <summary>Drops the items of the user that are not in the tree, and tells the user.</summary>
    private void PrunePicks()
    {
        if (Result is not { } result) return;
        var gone = _picks.Where(p => p.Kind == CleanupPickKind.Mine && result.NamesOf(p.Key) == null).ToList();
        if (gone.Count == 0) return;
        foreach (var p in gone) RemoveMine(p.Key);
        var names = gone.Select(p => result.DisplayPath(p.Key)).ToList();
        Notice = names.Count == 1
            ? $"{names[0]} is gone, so it left the cleanup list."
            : $"{string.Join(", ", names)} are gone, so they left the cleanup list.";
    }

    private void Changed() => SelectionChanged?.Invoke(this, EventArgs.Empty);

    // Texts

    public bool IsStale => Result is { } r && _now() - r.FinishedAt > StaleAfter;

    public string? StaleText
    {
        get
        {
            if (!IsStale) return null;
            var days = Math.Max(1, (int)((_now() - Result!.FinishedAt).TotalDays));
            return $"Scanned {days} {(days == 1 ? "day" : "days")} ago. Rescan before cleanup?";
        }
    }

    public string StatusText => Phase switch
    {
        DiskUsagePhase.Running => "Scanning… " + Elapsed(ScanStartedAt is DateTime s ? _now() - s : TimeSpan.Zero),
        DiskUsagePhase.Failed => "Scan failed",
        DiskUsagePhase.Cancelled => "Scan cancelled",
        DiskUsagePhase.Done when Result is { } r =>
            $"Scanned {Ago(_now() - r.FinishedAt)} · {Count(r.Files)} files · {Duration(TimeSpan.FromMilliseconds(r.DurationMs))}",
        _ => "Not scanned yet",
    };

    /// <summary>The remote job strip. Empty when there is no job.</summary>
    public IReadOnlyList<DiskJobStep> JobSteps
    {
        get
        {
            if (Job is not { } j) return Array.Empty<DiskJobStep>();
            var done = j.State == DiskScanJob.Done;
            var scanned = j.StartedTs is DateTime st && j.FinishedTs is DateTime ft ? Duration(ft - st) : "";
            return new[]
            {
                new DiskJobStep("Queued", Clock(j.CreatedTs), true),
                new DiskJobStep("Sampler picked up", Clock(j.StartedTs), j.StartedTs != null),
                new DiskJobStep("Scanned", done ? scanned : "", done),
                new DiskJobStep("Result in", done && j.ResultBytes > 0 ? DiskSize.Format(j.ResultBytes) : "", done),
            };
        }
    }

    public static string Elapsed(TimeSpan t)
        => $"{(int)t.TotalMinutes}:{t.Seconds:00}";

    public static string Duration(TimeSpan t)
        => t.TotalSeconds < 60 ? $"{Math.Max(0, (int)t.TotalSeconds)} s" : $"{(int)t.TotalMinutes} m {t.Seconds} s";

    public static string Ago(TimeSpan t)
    {
        if (t < TimeSpan.FromMinutes(1)) return "just now";
        if (t < TimeSpan.FromHours(1)) return $"{(int)t.TotalMinutes} min ago";
        if (t < TimeSpan.FromDays(1)) return $"{(int)t.TotalHours} h ago";
        var d = (int)t.TotalDays;
        return d == 1 ? "1 day ago" : $"{d} days ago";
    }

    public static string Count(long n)
    {
        var ci = CultureInfo.InvariantCulture;
        if (n >= 1_000_000) return (n / 1e6).ToString("0.#", ci) + "M";
        if (n >= 10_000) return (n / 1e3).ToString("0", ci) + "k";
        return n.ToString("N0", ci);
    }

    private static string Clock(DateTime? t)
        => t is DateTime v ? v.ToLocalTime().ToString("HH:mm:ss", CultureInfo.InvariantCulture) : "";
}
