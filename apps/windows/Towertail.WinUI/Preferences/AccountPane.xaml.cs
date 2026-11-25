using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Windows.System;
using DispatcherQueue = Microsoft.UI.Dispatching.DispatcherQueue;
using DispatcherQueueTimer = Microsoft.UI.Dispatching.DispatcherQueueTimer;

namespace Towertail.WinUI.Preferences;

/// <summary>The sign-in with an email code, and the account of the signed-in user. Mirrors the Mac Account pane.</summary>
public sealed partial class AccountPane : UserControl
{
    private readonly AppEnvironment _env;
    private readonly DispatcherQueue _ui = DispatcherQueue.GetForCurrentThread();
    private readonly DispatcherQueueTimer _tick;
    private bool _busy;
    private bool _leaving;

    private CloudSession Cloud => _env.Cloud;
    private CloudAccount Account => _env.Cloud.Account;

    public AccountPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        EmailBox.Text = Account.SavedEmail ?? "";
        _tick = _ui.CreateTimer();
        _tick.Interval = TimeSpan.FromSeconds(1);
        _tick.Tick += (_, _) => UpdateResend();
        Loaded += (_, _) =>
        {
            Cloud.StateChanged += OnStateChanged;
            _env.Nodes.Nodes.CollectionChanged += OnNodesChanged;
            _tick.Start();
            Refresh();
        };
        Unloaded += (_, _) =>
        {
            Cloud.StateChanged -= OnStateChanged;
            _env.Nodes.Nodes.CollectionChanged -= OnNodesChanged;
            _tick.Stop();
        };
        Refresh();
    }

    private void OnStateChanged() => _ui.TryEnqueue(Refresh);

    private void OnNodesChanged(object? sender, System.Collections.Specialized.NotifyCollectionChangedEventArgs e) => Refresh();

    private void Refresh()
    {
        if (Account.Me is { } me) ShowSignedIn(me);
        else ShowSignedOut();
#if DEBUG
        OpenDialogForDebug();
#endif
    }

#if DEBUG
    /// <summary>Renders: <c>TOWERTAIL_DEBUG_DIALOG</c> is <c>signout</c> or <c>delete</c>. The dialog opens once after the sign-in.</summary>
    private static string? s_debugDialog = System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_DIALOG");

    private void OpenDialogForDebug()
    {
        if (s_debugDialog is not { Length: > 0 } which || Account.Me is null || XamlRoot is null) return;
        s_debugDialog = null;
        if (which == "signout") OnSignOutClick(this, new RoutedEventArgs());
        else OnDeleteClick(this, new RoutedEventArgs());
    }
#endif

    private void ShowSignedOut()
    {
        InPanel.Visibility = Visibility.Collapsed;
        OutPanel.Visibility = Visibility.Visible;
        var pending = Account.PendingEmail;
        CodePanel.Visibility = pending is null ? Visibility.Collapsed : Visibility.Visible;
        if (pending is not null) SentText.Text = $"We sent a 6-digit code to {pending}.";
        if (Account.Status is CloudStatus.Failed failed)
        {
            ErrorText.Text = failed.Message;
            ErrorText.Visibility = Visibility.Visible;
        }
        else
        {
            ErrorText.Visibility = Visibility.Collapsed;
        }
        Code.IsEnabled = !_busy;
        UpdateSend();
        UpdateResend();
    }

    private void UpdateSend()
    {
        var email = EmailBox.Text.Trim();
        // The button asks for a code for a new address. A code for the same address goes through the link.
        SendBtn.IsEnabled = !_busy && email.Length > 0 && !string.Equals(email, Account.PendingEmail, StringComparison.Ordinal);
    }

    private void UpdateResend()
    {
        if (CodePanel.Visibility != Visibility.Visible) return;
        var wait = Account.ResendWait;
        ResendBtn.Content = AccountText.ResendText(wait);
        ResendBtn.IsEnabled = wait == TimeSpan.Zero && !_busy;
    }

    private void ShowSignedIn(CloudMe me)
    {
        OutPanel.Visibility = Visibility.Collapsed;
        InPanel.Visibility = Visibility.Visible;
        EmailValue.Text = me.User.Email;
        OrgValue.Text = me.Org.Name;
        PlanValue.Text = AccountText.PlanText(me, Account.Billing);
        var showHosts = Account.Billing is not null;
        HostsKey.Visibility = HostsRow.Visibility = showHosts ? Visibility.Visible : Visibility.Collapsed;
        if (Account.Billing is { } billing) HostsValue.Text = AccountText.HostsText(billing);
        MoveBtn.Visibility = _env.Move.CanMove ? Visibility.Visible : Visibility.Collapsed;
        SshValue.Text = AccountText.SshHostsText(_env.Nodes.Nodes.Count(n => n.Kind == NodeKind.Ssh && n.Cloud is null));
        SignOutBtn.IsEnabled = DeleteBtn.IsEnabled = !_leaving;
        LeavingRing.IsActive = _leaving;
        LeavingRing.Visibility = _leaving ? Visibility.Visible : Visibility.Collapsed;
    }

    private void OnEmailChanged(object sender, TextChangedEventArgs e) => UpdateSend();

    private void OnEmailKeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key == VirtualKey.Enter && SendBtn.IsEnabled) _ = SendCodeAsync();
    }

    private void OnSendClick(object sender, RoutedEventArgs e) => _ = SendCodeAsync();

    private void OnResendClick(object sender, RoutedEventArgs e) => _ = SendCodeAsync();

    private async Task SendCodeAsync()
    {
        if (_busy) return;
        _busy = true;
        UpdateSend();
        await Account.SendCodeAsync(EmailBox.Text);
        _busy = false;
        Refresh();
    }

    private void OnCodeCompleted(string code) => _ = VerifyAsync(code);

    private async Task VerifyAsync(string code)
    {
        if (_busy) return;
        _busy = true;
        Code.IsEnabled = false;
        await Account.VerifyAsync(code);
        _busy = false;
        Refresh();
        if (Account.Status is CloudStatus.Failed) Code.Clear();
        else await _env.Move.OfferOnceAsync();
    }

    private async void OnMoveClick(object sender, RoutedEventArgs e) => await _env.Move.OpenAsync();

    private async void OnDashboardClick(object sender, RoutedEventArgs e)
    {
        DashboardBtn.IsEnabled = false;
        try { await Launcher.LaunchUriAsync(await Account.OpenDashboardUriAsync()); }
        finally { DashboardBtn.IsEnabled = true; }
    }

    private async void OnSignOutClick(object sender, RoutedEventArgs e)
    {
        var exit = _env.Exit;
        var linked = exit.LinkedHosts.Count;
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = "Sign out of Towertail Cloud?",
            Content = new TextBlock { Text = AccountText.SignOutMessage(linked, exit.CloudOnlyHosts.Count), TextWrapping = TextWrapping.Wrap },
            PrimaryButtonText = "Move hosts back and sign out",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary,
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;
        _leaving = true;
        SignOutNote.Text = linked > 0 ? AccountText.LeavingText(linked) : "The app signs out.";
        Refresh();
        try
        {
            while (true)
            {
                await exit.LeaveAsync();
                if (exit.Phase != ExitPhase.Failed) break;
                var failure = new ContentDialog
                {
                    XamlRoot = XamlRoot,
                    Title = "Some hosts did not move back",
                    Content = new TextBlock { Text = AccountText.LeaveFailureText(exit.Error), TextWrapping = TextWrapping.Wrap },
                    PrimaryButtonText = "Try again",
                    SecondaryButtonText = "Sign out anyway",
                    CloseButtonText = "Cancel",
                    DefaultButton = ContentDialogButton.Primary,
                };
                var choice = await failure.ShowAsync();
                if (choice == ContentDialogResult.Primary) { exit.Reset(); continue; }
                if (choice == ContentDialogResult.Secondary) await exit.SignOutAnywayAsync();
                else exit.Reset();
                break;
            }
        }
        finally
        {
            _leaving = false;
            SignOutNote.Text = "The app moves your Cloud hosts back to SSH first.";
            Refresh();
        }
    }

    private async void OnDeleteClick(object sender, RoutedEventArgs e)
        => await DeleteCloudAccountDialog.ShowAsync(XamlRoot, _env.Exit);
}
