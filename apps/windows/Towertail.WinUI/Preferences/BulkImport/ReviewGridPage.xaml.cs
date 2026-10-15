using System.Collections.ObjectModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

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

    private void OnApplyAuth(object sender, RoutedEventArgs e)
    {
        var authMethod = BulkAuthBox.SelectedIndex == 1 ? AuthMethod.Password : AuthMethod.Key;
        foreach (var r in _rows) r.AuthMethod = authMethod;
    }

    private void OnPasswordChanged(object sender, RoutedEventArgs e)
    {
        // PasswordBox.Password isn't a DependencyProperty, so x:Bind TwoWay
        // silently no-ops. Push the current value into the row manually.
        if (sender is PasswordBox pb && pb.Tag is ImportRow row)
        {
            row.Password = pb.Password;
        }
    }

    private async void OnKeyHelp(object sender, RoutedEventArgs e)
    {
        var (user, host) = (sender is FrameworkElement fe && fe.Tag is ImportRow row)
            ? (row.SshUser, row.SshHost) : ("", "");
        await Preferences.SshKeySetupDialog.ShowAsync(
            this.XamlRoot, user: user, host: host);
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

        var node = row.ToNode();

        // For password rows, stash the typed password in DPAPI before the test
        // connect — SshConnectionFactory pulls the password from DPAPI and the
        // row hasn't been deployed yet. Cleaned up on failure.
        var stashedPassword = false;
        if (row.AuthMethod == AuthMethod.Password && !string.IsNullOrEmpty(row.Password))
        {
            DpapiStore.SetPassword(row.Id, row.Password);
            stashedPassword = true;
        }

        try
        {
            // Bulk-import flow auto-trusts host keys — the user already
            // declared intent to add these hosts, so asking them to confirm
            // each fingerprint mid-wizard is noise. Capture the accepted
            // fingerprint onto the row so the post-deploy collector connect
            // sees a pinned key and doesn't re-prompt.
            HostKeyPrompt autoTrust = (_, _) => Task.FromResult(true);
            string? acceptedFingerprint = null;
            Action<Guid, string> capture = (_, fp) => acceptedFingerprint = fp;
            var bootstrap = new SshBootstrap(
                node,
                hostKeyPrompt: autoTrust,
                onTrust: capture);
            var triple = await bootstrap.DetectAsync().ConfigureAwait(true);
            if (triple.Os == SshBootstrap.RemoteOs.Unknown)
            {
                row.StatusDetail = $"unknown OS ({triple.Triple})";
                row.Status = ImportRow.RowStatus.Failed;
                return;
            }
            var deployed = await bootstrap.DeployAsync(triple).ConfigureAwait(true);
            if (!deployed)
            {
                row.StatusDetail = "deploy failed (sftp)";
                row.Status = ImportRow.RowStatus.Failed;
                return;
            }
            var verified = await bootstrap.VerifyAsync(triple).ConfigureAwait(true);
            row.StatusDetail = verified ? $"reachable ({triple.Triple})" : "self-check failed";
            row.Status = verified ? ImportRow.RowStatus.Ok : ImportRow.RowStatus.Failed;
            if (verified && acceptedFingerprint is not null)
            {
                row.KnownHostFingerprint = acceptedFingerprint;
            }
        }
        catch (OperationCanceledException)
        {
            row.StatusDetail = "timed out (ssh unreachable?)";
            row.Status = ImportRow.RowStatus.Failed;
            if (stashedPassword) DpapiStore.DeletePassword(row.Id);
        }
        catch (Exception ex)
        {
            // SSH.NET often wraps the real cause — show both top-level and
            // inner-most so the user can see e.g. "algo mismatch" or
            // "Permission denied (password)".
            row.StatusDetail = FirstLine(FlattenMessage(ex), 200);
            row.Status = ImportRow.RowStatus.Failed;
            _env.Logger.Error($"bulk-import test failed for {row.SshHost}", ex);
            if (stashedPassword) DpapiStore.DeletePassword(row.Id);
        }
    }

    private static string FlattenMessage(Exception ex)
    {
        var msgs = new List<string>();
        for (var e = ex; e != null; e = e.InnerException)
        {
            msgs.Add($"{e.GetType().Name}: {e.Message}");
        }
        return string.Join(" → ", msgs);
    }

    private static string FirstLine(string s, int max)
    {
        var line = s.Split('\n').FirstOrDefault(l => !string.IsNullOrWhiteSpace(l))?.Trim() ?? "";
        return line.Length > max ? line[..max] + "…" : line;
    }

}
