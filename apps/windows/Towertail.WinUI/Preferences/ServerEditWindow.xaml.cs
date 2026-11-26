using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Windows.Graphics;

namespace Towertail.WinUI.Preferences;

public sealed partial class ServerEditWindow : Window
{
    private Node? _existing;
    private Action<Node?>? _onDone;
    private readonly AppEnvironment _env;
    private AuthMethod _originalAuthMethod = AuthMethod.Key;
    private string? _knownHostFingerprint;

    private readonly List<ThresholdOverrideRow> _thresholdRows = new();
    private readonly List<(CheckBox Box, AlertSignal Signal)> _mutes = new();

    private static readonly (AlertSignal Signal, string Title)[] MuteTitles =
    {
        (AlertSignal.Cpu, "CPU"), (AlertSignal.Mem, "Memory"), (AlertSignal.Disk, "Disk"),
        (AlertSignal.Procs, "Processes"), (AlertSignal.Zombies, "Zombies"), (AlertSignal.Pids, "PID use"),
        (AlertSignal.Files, "Open files"), (AlertSignal.Inodes, "Inodes"),
        (AlertSignal.MemPressure, "Memory pressure"), (AlertSignal.IoPressure, "I/O pressure"),
    };

    public ServerEditWindow()
    {
        _env = Towertail.WinUI.App.Current.Environment;
        InitializeComponent();
        Title = "Server";
        AppWindow.Resize(new SizeInt32(560, 860));
        foreach (var m in ThresholdMetrics.Usage) AddThresholdRow(ThresholdRows, m);
        foreach (var m in ThresholdMetrics.Health) AddThresholdRow(HealthRows, m);
    }

    private void AddThresholdRow(StackPanel panel, ThresholdMetric m)
    {
        var row = new ThresholdOverrideRow(m);
        _thresholdRows.Add(row);
        panel.Children.Add(row);
    }

    /// <summary>A globally muted signal shows checked and disabled.</summary>
    private void BindMutes(MutedSignals node)
    {
        var global = _env.ServerSettings.MutedSignals;
        var remote = _env.Backend is RemoteBackend;
        MuteGrid.Children.Clear();
        _mutes.Clear();
        foreach (var (signal, title) in MuteTitles)
        {
            var inherited = global.Contains(signal);
            var box = new CheckBox
            {
                Content = title,
                IsChecked = inherited || node.Contains(signal),
                IsEnabled = !inherited && !remote,
            };
            if (inherited) ToolTipService.SetToolTip(box, "Muted on all servers");
            MuteGrid.Children.Add(box);
            _mutes.Add((box, signal));
        }
        if (remote) MuteHeader.Text = "Mute notifications: Not supported by this server";
    }

    /// <summary>A disabled box keeps the host's own value.</summary>
    private MutedSignals SelectedMutes()
    {
        var own = _existing?.MutedSignals ?? MutedSignals.None;
        return new(_mutes.Where(m => m.Box.IsEnabled ? m.Box.IsChecked == true : own.Contains(m.Signal)).Select(m => m.Signal));
    }

    public void Bind(Node? n, Action<Node?> onDone)
    {
        _existing = n;
        _onDone = onDone;
        TitleText.Text = n is null ? "New Server" : "Edit Server";
        Title = TitleText.Text;

        DisplayNameBox.Text = n?.DisplayName ?? "";
        KindBox.SelectedIndex = n?.Kind == NodeKind.Ssh ? 1 : 0;
        UserBox.Text = n?.SshUser ?? Environment.UserName;
        HostBox.Text = n?.SshHost ?? "";
        // NumberBox accepts NaN as "empty"; we render placeholder 22 then.
        PortBox.Value = n?.SshPort is int p ? p : double.NaN;
        AuthMethodBox.SelectedIndex = (n?.AuthMethod ?? AuthMethod.Key) == AuthMethod.Password ? 1 : 0;
        _originalAuthMethod = n?.AuthMethod ?? AuthMethod.Key;
        _knownHostFingerprint = n?.KnownHostFingerprint;

        // Seed password from DPAPI so the field isn't blank on edit. Only
        // done for password auth; for key auth the field stays hidden.
        if (n is not null && n.AuthMethod == AuthMethod.Password)
        {
            var pw = DpapiStore.GetPassword(n.Id);
            if (!string.IsNullOrEmpty(pw)) PasswordBoxField.Password = pw;
        }

        BindCloudRules(n);
        BindMutes(n?.MutedSignals ?? MutedSignals.None);
        UpdatePasswordVisibility();
        UpdateHostKeyPanel();

        TagsBox.Text = n is { Tags.Count: > 0 } ? string.Join(", ", n.Tags) : "";
        EnabledCheck.IsChecked = n?.Enabled ?? true;
        IconWarnCheck.IsChecked = n?.IconOnWarn ?? true;
        IconCriticalCheck.IsChecked = n?.IconOnCritical ?? true;
        NotifyWarnCheck.IsChecked = n?.NotifyOnWarn ?? true;
        NotifyCriticalCheck.IsChecked = n?.NotifyOnCritical ?? true;

        var global = _env.ServerSettings.Thresholds;
        foreach (var row in _thresholdRows)
            row.Bind(global.Get(row.Metric), n?.ThresholdOverrides?.Get(row.Metric));

        AlertsEditor.Rules = n?.CustomAlerts ?? _env.ServerSettings.Alerts;
        CustomAlertsCheck.IsChecked = n?.CustomAlerts != null;
        AlertsEditor.IsEnabled = CustomAlertsCheck.IsChecked == true;

        UpdateSshGroupVisibility();
    }

    /// <summary>A Cloud host takes its rules from the cloud. The local rule boxes give way to the cloud rules view.</summary>
    private void BindCloudRules(Node? n)
    {
        CloudRulesHost.Children.Clear();
        var cloud = n is { IsCloud: true };
        LocalRules.Visibility = cloud ? Visibility.Collapsed : Visibility.Visible;
        CloudRulesHost.Visibility = cloud ? Visibility.Visible : Visibility.Collapsed;
        FormPanel.MaxWidth = cloud ? 760 : 520;
        if (!cloud) return;
        if (_env.Cloud.Rules is { } rules)
        {
            var model = new CloudHostRulesModel(rules, n!.Id, () => _env.Cloud.Account.Me, () => _env.Servers.Find(n.Id)?.HostOs);
            Action reload = () => DispatcherQueue.TryEnqueue(model.Reload);
            rules.Changed += reload;
            Closed += (_, _) => rules.Changed -= reload;
            CloudRulesHost.Children.Add(new CloudHostRulesView(model));
        }
        else
        {
            CloudRulesHost.Children.Add(new TextBlock { Text = "Sign in to Cloud to see the rules of this host.", Opacity = 0.7 });
        }
        AppWindow.Resize(new SizeInt32(800, 860));
    }

    /// <summary>Scrolls the form to the cloud rules. The debug renders use it.</summary>
    public void ScrollToCloudRules()
        => CloudRulesHost.StartBringIntoView(new BringIntoViewOptions { AnimationDesired = false, VerticalAlignmentRatio = 0 });

    private void OnKindChanged(object sender, SelectionChangedEventArgs e) => UpdateSshGroupVisibility();

    private void UpdateSshGroupVisibility()
        => SshGroup.Visibility = KindBox.SelectedIndex == 1 ? Visibility.Visible : Visibility.Collapsed;

    private void OnAuthMethodChanged(object sender, SelectionChangedEventArgs e)
        => UpdatePasswordVisibility();

    private void UpdatePasswordVisibility()
    {
        PasswordBoxField.Visibility =
            AuthMethodBox.SelectedIndex == 1 ? Visibility.Visible : Visibility.Collapsed;
    }

    private void UpdateHostKeyPanel()
    {
        if (string.IsNullOrEmpty(_knownHostFingerprint))
        {
            HostKeyPanel.Visibility = Visibility.Collapsed;
            return;
        }
        HostKeyPanel.Visibility = Visibility.Visible;
        HostKeyText.Text = _knownHostFingerprint;
    }

    private void OnForgetHostKey(object sender, RoutedEventArgs e)
    {
        _knownHostFingerprint = null;
        UpdateHostKeyPanel();
    }

    private async void OnKeyHelp(object sender, RoutedEventArgs e)
    {
        if (Content is FrameworkElement fe)
        {
            await SshKeySetupDialog.ShowAsync(
                fe.XamlRoot,
                user: UserBox.Text.Trim(),
                host: HostBox.Text.Trim());
        }
    }

    private void OnCustomAlertsToggled(object sender, RoutedEventArgs e)
        => AlertsEditor.IsEnabled = CustomAlertsCheck.IsChecked == true;

    private void OnCancel(object sender, RoutedEventArgs e) { _onDone?.Invoke(null); Close(); }

    private void OnOk(object sender, RoutedEventArgs e)
    {
        // KindBox has no Cloud entry: a cloud-only host keeps its kind.
        var kind = _existing?.Kind == NodeKind.Cloud ? NodeKind.Cloud
            : KindBox.SelectedIndex == 1 ? NodeKind.Ssh : NodeKind.Local;
        var tags = TagsBox.Text
            .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            .ToList();

        var cloudHost = _existing is { IsCloud: true };
        var overrides = cloudHost
            ? _existing!.ThresholdOverrides ?? new ThresholdOverrides()
            : _thresholdRows.Aggregate(new ThresholdOverrides(), (o, row) => o.With(row.Metric, row.Value));

        int? port = null;
        if (kind == NodeKind.Ssh && !double.IsNaN(PortBox.Value))
        {
            var p = (int)Math.Round(PortBox.Value);
            if (p is >= 1 and <= 65535) port = p;
        }

        var authMethod = kind == NodeKind.Ssh
            ? (AuthMethodBox.SelectedIndex == 1 ? AuthMethod.Password : AuthMethod.Key)
            : AuthMethod.Key;

        var nodeId = _existing?.Id ?? Guid.NewGuid();
        var node = new Node
        {
            Id = nodeId,
            DisplayName = DisplayNameBox.Text.Trim(),
            Kind = kind,
            Cloud = _existing?.Cloud,
            SshUser = kind == NodeKind.Ssh ? UserBox.Text.Trim() : null,
            SshHost = kind == NodeKind.Ssh ? HostBox.Text.Trim() : null,
            SshPort = port,
            AuthMethod = authMethod,
            KnownHostFingerprint = kind == NodeKind.Ssh ? _knownHostFingerprint : null,
            Tags = tags,
            Enabled = EnabledCheck.IsChecked == true,
            IconOnWarn = IconWarnCheck.IsChecked == true,
            IconOnCritical = IconCriticalCheck.IsChecked == true,
            NotifyOnWarn = NotifyWarnCheck.IsChecked == true,
            NotifyOnCritical = NotifyCriticalCheck.IsChecked == true,
            ThresholdOverrides = overrides.IsEmpty ? null : overrides,
            CustomAlerts = cloudHost ? _existing!.CustomAlerts : CustomAlertsCheck.IsChecked == true ? AlertsEditor.Rules : null,
            Favorite = _existing?.Favorite ?? false,
            SnoozedUntil = _existing?.SnoozedUntil,
            CustomLongProc = _existing?.CustomLongProc,
            IgnoredProcs = _existing?.IgnoredProcs ?? Array.Empty<string>(),
            MutedSignals = cloudHost ? _existing!.MutedSignals : SelectedMutes(),
        };

        // DPAPI handling: store on password auth, delete on password → key (or away from SSH).
        if (kind == NodeKind.Ssh && authMethod == AuthMethod.Password &&
            !string.IsNullOrEmpty(PasswordBoxField.Password))
        {
            DpapiStore.SetPassword(nodeId, PasswordBoxField.Password);
        }
        else if (_originalAuthMethod == AuthMethod.Password &&
                 (authMethod != AuthMethod.Password || kind != NodeKind.Ssh))
        {
            DpapiStore.DeletePassword(nodeId);
        }

        _onDone?.Invoke(node);
        Close();
    }
}
