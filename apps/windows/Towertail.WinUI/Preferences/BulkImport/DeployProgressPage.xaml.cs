using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Runtime.CompilerServices;
using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Preferences.BulkImport;

/// <summary>
/// Row-level progress view model for the deploy step. Each row runs through
/// bootstrap-and-verify; the UI reflects per-row state live.
/// </summary>
public sealed class DeployRow : INotifyPropertyChanged
{
    public event PropertyChangedEventHandler? PropertyChanged;

    public required Guid Id { get; init; }
    public required string DisplayName { get; init; }
    public required string UserAtHost { get; init; }
    public required Node Node { get; init; }
    public required bool IsSsh { get; init; }

    public enum Stage { Pending, Working, Ok, Failed }

    private Stage _stage = Stage.Pending;
    public Stage StatusStage
    {
        get => _stage;
        set
        {
            if (_stage == value) return;
            _stage = value;
            Notify();
            Notify(nameof(StatusGlyph));
            Notify(nameof(StatusBrush));
        }
    }

    private string _detail = "pending";
    public string StatusDetail
    {
        get => _detail;
        set { if (_detail != value) { _detail = value; Notify(); } }
    }

    public string StatusGlyph => _stage switch
    {
        Stage.Ok => "",       // checkmark circle
        Stage.Failed => "",   // error badge
        Stage.Working => "",  // sync / in-progress
        _ => "",              // circle (pending)
    };

    public Brush StatusBrush => _stage switch
    {
        Stage.Ok => new SolidColorBrush(Colors.SeaGreen),
        Stage.Failed => new SolidColorBrush(Colors.IndianRed),
        Stage.Working => new SolidColorBrush(Colors.DodgerBlue),
        _ => new SolidColorBrush(Colors.Gray),
    };

    private void Notify([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name ?? ""));
}

public sealed partial class DeployProgressPage : Page
{
    private readonly ObservableCollection<DeployRow> _rows = new();

    public DeployProgressPage()
    {
        InitializeComponent();
        Items.ItemsSource = _rows;
    }

    public DeployProgressPage(BulkImportState state, AppEnvironment env) : this()
    {
        _ = RunAsync(state, env);
    }

    /// <summary>Event fired once deploy is finished so the wizard can flip its
    /// Next button into a "Done" state. The wizard already renders Deploy as
    /// the final step; we just need it to know we're finished.</summary>
    public event Action? Finished;

    private async Task RunAsync(BulkImportState state, AppEnvironment env)
    {
        var candidates = state.Candidates
            .Where(r => r.Included && !r.IsDuplicate && r.IsValid())
            .ToList();

        // Stash passwords first — bootstrap pulls from DPAPI.
        foreach (var r in candidates)
        {
            if (r.Kind == NodeKind.Ssh && r.AuthMethod == AuthMethod.Password
                && !string.IsNullOrEmpty(r.Password))
            {
                DpapiStore.SetPassword(r.Id, r.Password);
            }
        }

        foreach (var r in candidates)
        {
            var node = r.ToNode();
            _rows.Add(new DeployRow
            {
                Id = r.Id,
                DisplayName = r.DisplayName,
                UserAtHost = r.Kind == NodeKind.Ssh
                    ? $"{r.SshUser}@{r.SshHost}"
                    : "local",
                Node = node,
                IsSsh = r.Kind == NodeKind.Ssh,
            });
        }
        UpdateCounters();

        // Batch of 4 concurrent bootstraps.
        const int batch = 4;
        for (int i = 0; i < _rows.Count; i += batch)
        {
            var slice = _rows.Skip(i).Take(batch).Select(row => DeployOne(row, env));
            await Task.WhenAll(slice);
        }

        Ring.IsActive = false;
        var ok = _rows.Count(r => r.StatusStage == DeployRow.Stage.Ok);
        var failed = _rows.Count(r => r.StatusStage == DeployRow.Stage.Failed);
        if (failed == 0)
        {
            StatusText.Text = $"Deployed {ok} of {_rows.Count}.";
        }
        else
        {
            StatusText.Text = $"Deployed {ok} of {_rows.Count} · {failed} failed.";
        }
        UpdateCounters();
        Finished?.Invoke();
    }

    private void UpdateCounters()
    {
        var ok = _rows.Count(r => r.StatusStage == DeployRow.Stage.Ok);
        var failed = _rows.Count(r => r.StatusStage == DeployRow.Stage.Failed);
        var pending = _rows.Count - ok - failed;
        Counters.Text = $"{ok} ok · {failed} failed · {pending} remaining";
    }

    private async Task DeployOne(DeployRow row, AppEnvironment env)
    {
        row.StatusStage = DeployRow.Stage.Working;
        row.StatusDetail = "connecting…";
        UpdateCounters();

        // Local nodes: no SSH bootstrap, just commit.
        if (!row.IsSsh)
        {
            CommitNode(row.Node, env);
            row.StatusStage = DeployRow.Stage.Ok;
            row.StatusDetail = "local";
            UpdateCounters();
            return;
        }

        try
        {
            HostKeyPrompt autoTrust = (_, _) => Task.FromResult(true);
            string? fingerprint = null;
            Action<Guid, string> capture = (_, fp) => fingerprint = fp;
            var bootstrap = new SshBootstrap(row.Node, hostKeyPrompt: autoTrust, onTrust: capture);

            row.StatusDetail = "detecting…";
            var triple = await bootstrap.DetectAsync().ConfigureAwait(true);
            if (triple.Os == SshBootstrap.RemoteOs.Unknown)
            {
                row.StatusStage = DeployRow.Stage.Failed;
                row.StatusDetail = $"unknown OS ({triple.Triple})";
                UpdateCounters();
                return;
            }

            row.StatusDetail = "uploading sampler…";
            if (!await bootstrap.DeployAsync(triple).ConfigureAwait(true))
            {
                row.StatusStage = DeployRow.Stage.Failed;
                row.StatusDetail = "deploy failed (sftp)";
                UpdateCounters();
                return;
            }

            row.StatusDetail = "verifying…";
            if (!await bootstrap.VerifyAsync(triple).ConfigureAwait(true))
            {
                row.StatusStage = DeployRow.Stage.Failed;
                row.StatusDetail = "self-check failed";
                UpdateCounters();
                return;
            }

            // Pin the captured fingerprint so the collector's first poll
            // doesn't re-prompt.
            var node = row.Node with { KnownHostFingerprint = fingerprint };
            CommitNode(node, env);
            row.StatusStage = DeployRow.Stage.Ok;
            row.StatusDetail = $"deployed ({triple.Triple})";
        }
        catch (Exception ex)
        {
            row.StatusStage = DeployRow.Stage.Failed;
            row.StatusDetail = FirstLine(ex.InnerException?.Message ?? ex.Message, 140);
            env.Logger.Error($"bulk-deploy failed for {row.UserAtHost}", ex);
        }
        UpdateCounters();
    }

    private static void CommitNode(Node node, AppEnvironment env)
    {
        // NodeStore.Add mutates an ObservableCollection bound to UI — must run
        // on the UI thread. Awaited via .Add synchronously since we're called
        // from an async continuation already on the UI thread.
        env.Nodes.Add(node);
    }

    private static string FirstLine(string s, int max)
    {
        var line = s.Split('\n').FirstOrDefault(l => !string.IsNullOrWhiteSpace(l))?.Trim() ?? "";
        return line.Length > max ? line[..max] + "…" : line;
    }
}
