using System.Collections.ObjectModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class ReviewGridPage : Page
{
    private readonly BulkImportState _state;
    private readonly AppEnvironment? _env;
    private readonly ObservableCollection<ImportRow> _rows = new();

    public ReviewGridPage() { InitializeComponent(); _state = new(); }

    public ReviewGridPage(BulkImportState state, AppEnvironment env) : this()
    {
        _state = state;
        _env = env;
        BulkUserBox.Text = Environment.UserName;

        foreach (var r in _state.Candidates) _rows.Add(r);
        Items.ItemsSource = _rows;

        MarkDuplicates();
        UpdateFooter();
        foreach (var r in _rows) r.PropertyChanged += (_, e) =>
        {
            if (e.PropertyName == nameof(ImportRow.Included)) UpdateFooter();
        };
    }

    private void MarkDuplicates()
    {
        if (_env is null) return;
        var existing = _env.Nodes.Nodes;
        foreach (var r in _rows)
        {
            var host = r.SshHost.Trim().ToLowerInvariant();
            var name = r.DisplayName.Trim().ToLowerInvariant();
            var dup = existing.Any(n =>
            {
                var nHost = (n.SshHost ?? "").Trim().ToLowerInvariant();
                if (!string.IsNullOrEmpty(host) && nHost == host) return true;
                return !string.IsNullOrEmpty(name) && n.DisplayName.ToLowerInvariant() == name;
            });
            if (dup)
            {
                r.IsDuplicate = true;
                r.Included = false;
                r.Status = ImportRow.RowStatus.Duplicate;
            }
        }
    }

    private void UpdateFooter()
    {
        var selected = _rows.Count(r => r.Included);
        FooterText.Text = $"{selected} of {_rows.Count} selected";
        ToggleAllBtn.Content = _rows.All(r => r.Included) ? "Deselect all" : "Select all";
    }

    private void OnToggleAll(object sender, RoutedEventArgs e)
    {
        var anyOff = _rows.Any(r => !r.Included);
        foreach (var r in _rows) r.Included = anyOff;
        UpdateFooter();
    }

    private void OnApplyBulk(object sender, RoutedEventArgs e)
    {
        var user = BulkUserBox.Text.Trim();
        if (string.IsNullOrEmpty(user)) return;
        var blanksOnly = ApplyScopeBox.SelectedIndex == 0;
        foreach (var r in _rows)
        {
            if (blanksOnly && !string.IsNullOrWhiteSpace(r.SshUser)) continue;
            r.SshUser = user;
        }
    }

    private void OnRemoveRow(object sender, RoutedEventArgs e)
    {
        if (sender is FrameworkElement fe && fe.Tag is ImportRow row)
        {
            _rows.Remove(row);
            _state.Candidates = _rows.ToList();
            _state.NotifyCandidatesChanged();
            UpdateFooter();
        }
    }

    private async void OnTestRow(object sender, RoutedEventArgs e)
    {
        if (sender is FrameworkElement fe && fe.Tag is ImportRow row)
        {
            await TestAsync(row).ConfigureAwait(false);
        }
    }

    private async void OnTestAll(object sender, RoutedEventArgs e)
    {
        TestAllBtn.IsEnabled = false;
        try
        {
            var targets = _rows.Where(r => r.Included && r.Kind == NodeKind.Ssh && r.IsValid()).ToList();
            // Cap at 4 concurrent tests to avoid saturating the network.
            const int batch = 4;
            for (int i = 0; i < targets.Count; i += batch)
            {
                var slice = targets.Skip(i).Take(batch).Select(TestAsync);
                await Task.WhenAll(slice);
            }
        }
        finally
        {
            TestAllBtn.IsEnabled = true;
        }
    }

    private async Task TestAsync(ImportRow row)
    {
        if (_env is null || row.Kind != NodeKind.Ssh || !row.IsValid()) return;
        row.Status = ImportRow.RowStatus.Testing;
        row.StatusDetail = "";

        var runner = new DefaultProcessRunner();
        var node = row.ToNode();

        try
        {
            // Probe ssh directly first so we can surface the real stderr / exit code
            // rather than a generic "A task was canceled" if the bootstrap times out.
            var sshExe = SshSamplerInvoker.DefaultSshPath();
            var probeArgs = new List<string>
            {
                "-o", "BatchMode=yes",
                "-o", "ConnectTimeout=10",
                node.UserAtHost!,
                "uname -sm || ver",
            };
            var probe = await runner.RunAsync(
                new ProcessRequest(sshExe, probeArgs, Timeout: TimeSpan.FromSeconds(20)))
                .ConfigureAwait(true);
            if (!probe.Ok)
            {
                var err = probe.StdErr.Trim();
                if (string.IsNullOrEmpty(err)) err = $"ssh exited {probe.ExitCode}";
                row.StatusDetail = FirstLine(err, 120);
                row.Status = ImportRow.RowStatus.Failed;
                return;
            }
            var triple = SshBootstrap.ParseUnameOrVer((probe.StdOut + "\n" + probe.StdErr).Trim());
            if (triple.Os == SshBootstrap.RemoteOs.Unknown)
            {
                row.StatusDetail = $"unknown OS: {FirstLine(probe.StdOut + probe.StdErr, 60)}";
                row.Status = ImportRow.RowStatus.Failed;
                return;
            }

            var bootstrap = new SshBootstrap(runner, node);
            var deployed = await bootstrap.DeployAsync(triple).ConfigureAwait(true);
            if (!deployed)
            {
                row.StatusDetail = "deploy failed (scp)";
                row.Status = ImportRow.RowStatus.Failed;
                return;
            }
            var verified = await bootstrap.VerifyAsync(triple).ConfigureAwait(true);
            row.StatusDetail = verified ? $"reachable ({triple.Triple})" : "self-check failed";
            row.Status = verified ? ImportRow.RowStatus.Ok : ImportRow.RowStatus.Failed;
        }
        catch (OperationCanceledException)
        {
            row.StatusDetail = "timed out (ssh unreachable?)";
            row.Status = ImportRow.RowStatus.Failed;
        }
        catch (Exception ex)
        {
            row.StatusDetail = FirstLine(ex.Message, 120);
            row.Status = ImportRow.RowStatus.Failed;
        }
    }

    private static string FirstLine(string s, int max)
    {
        var line = s.Split('\n').FirstOrDefault(l => !string.IsNullOrWhiteSpace(l))?.Trim() ?? "";
        return line.Length > max ? line[..max] + "…" : line;
    }
}
