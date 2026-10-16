using System.Globalization;
using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.Collectors;

namespace Towertail.WinUI.State;

public enum DiskUsagePhase { Idle, Running, Done, Failed, Cancelled }

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
    private CancellationTokenSource? _cts;

    [ObservableProperty] private DiskUsagePhase _phase = DiskUsagePhase.Idle;
    [ObservableProperty] private DiskScanProgress? _progress;
    /// <summary>The last result. While a new scan runs, this is the previous one.</summary>
    [ObservableProperty] private DiskScanResult? _result;
    [ObservableProperty] private DiskScanJob? _job;
    [ObservableProperty] private string? _error;
    [ObservableProperty] private string _scope;
    [ObservableProperty] private DateTime? _scanStartedAt;

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
    public event EventHandler? SelectionChanged;

    /// <summary>Show the newest kept result of the current scope.</summary>
    public void LoadLatest()
    {
        if (_store?.Latest(_node.Id, Scope) is not { } entry) return;
        Result = entry.Result;
        Phase = DiskUsagePhase.Done;
        SelectSafe();
    }

    public Task StartAsync()
    {
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

    // Selection

    public void Toggle(string recId)
    {
        if (!_selected.Remove(recId)) _selected.Add(recId);
        SelectionChanged?.Invoke(this, EventArgs.Empty);
    }

    public void SelectSafe()
    {
        _selected.Clear();
        foreach (var r in Result?.Recommendations ?? Array.Empty<DiskRec>())
            if (r.IsSafe) _selected.Add(r.Id);
        SelectionChanged?.Invoke(this, EventArgs.Empty);
    }

    public void ClearSelection()
    {
        _selected.Clear();
        SelectionChanged?.Invoke(this, EventArgs.Empty);
    }

    public long SelectedBytes => Result?.Recommendations.Where(r => _selected.Contains(r.Id)).Sum(r => r.Bytes) ?? 0;

    /// <summary>Tree node ids that the selected recommendations cover.</summary>
    public IReadOnlySet<string> SelectedNodeIds => Result?.Recommendations
        .Where(r => _selected.Contains(r.Id))
        .SelectMany(r => r.NodeIds ?? Array.Empty<string>())
        .ToHashSet() ?? new HashSet<string>();

    public CleanupScript? Script => Result is { } r ? new CleanupScript(r, _selected, _now()) : null;

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
