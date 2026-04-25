using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Windows.Graphics;
using WinRT.Interop;

namespace Towertail.WinUI.Preferences;

public sealed partial class ServerEditWindow : Window
{
    private Node? _existing;
    private Action<Node?>? _onDone;
    private readonly AppEnvironment _env;
    private AuthMethod _originalAuthMethod = AuthMethod.Key;
    private string? _knownHostFingerprint;

    private Slider[] _thresholdSliders = Array.Empty<Slider>();

    public ServerEditWindow()
    {
        _env = Towertail.WinUI.App.Current.Environment;
        InitializeComponent();
        Title = "Server";
        ResizeInitial(560, 760);
        _thresholdSliders = new[]
        {
            CpuWarnSlider, CpuCriticalSlider,
            MemWarnSlider, MemCriticalSlider,
            DiskWarnSlider, DiskCriticalSlider,
        };
        SetThresholdsEnabled(false);
        HookSliders();
    }

    private void SetThresholdsEnabled(bool enabled)
    {
        foreach (var s in _thresholdSliders) s.IsEnabled = enabled;
    }

    private void ResizeInitial(int w, int h)
    {
        try
        {
            var hwnd = WindowNative.GetWindowHandle(this);
            var id = Win32Interop.GetWindowIdFromWindow(hwnd);
            AppWindow.GetFromWindowId(id)?.Resize(new SizeInt32(w, h));
        }
        catch { }
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

        UpdatePasswordVisibility();
        UpdateHostKeyPanel();

        TagsBox.Text = n is { Tags.Count: > 0 } ? string.Join(", ", n.Tags) : "";
        EnabledCheck.IsChecked = n?.Enabled ?? true;
        IconWarnCheck.IsChecked = n?.IconOnWarn ?? true;
        IconCriticalCheck.IsChecked = n?.IconOnCritical ?? true;
        NotifyWarnCheck.IsChecked = n?.NotifyOnWarn ?? true;
        NotifyCriticalCheck.IsChecked = n?.NotifyOnCritical ?? true;

        var t = n?.CustomThresholds ?? _env.ServerSettings.Thresholds;
        SetSliderPct(CpuWarnSlider, CpuWarnValue, t.CpuWarn);
        SetSliderPct(CpuCriticalSlider, CpuCriticalValue, t.CpuCritical);
        SetSliderPct(MemWarnSlider, MemWarnValue, t.MemWarn);
        SetSliderPct(MemCriticalSlider, MemCriticalValue, t.MemCritical);
        SetSliderPct(DiskWarnSlider, DiskWarnValue, t.DiskWarn);
        SetSliderPct(DiskCriticalSlider, DiskCriticalValue, t.DiskCritical);

        CustomThresholdsCheck.IsChecked = n?.CustomThresholds != null;
        SetThresholdsEnabled(CustomThresholdsCheck.IsChecked == true);
        UpdateSshGroupVisibility();
    }

    private void HookSliders()
    {
        (Slider s, TextBlock lbl)[] pairs =
        {
            (CpuWarnSlider, CpuWarnValue),
            (CpuCriticalSlider, CpuCriticalValue),
            (MemWarnSlider, MemWarnValue),
            (MemCriticalSlider, MemCriticalValue),
            (DiskWarnSlider, DiskWarnValue),
            (DiskCriticalSlider, DiskCriticalValue),
        };
        foreach (var (s, lbl) in pairs)
            s.ValueChanged += (_, e) => lbl.Text = $"{(int)Math.Round(e.NewValue)}%";
    }

    private static void SetSliderPct(Slider s, TextBlock lbl, double frac)
    {
        var pct = Math.Clamp(frac * 100.0, s.Minimum, s.Maximum);
        s.Value = pct;
        lbl.Text = $"{(int)Math.Round(pct)}%";
    }

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

    private void OnCustomToggled(object sender, RoutedEventArgs e)
        => SetThresholdsEnabled(CustomThresholdsCheck.IsChecked == true);

    private void OnCancel(object sender, RoutedEventArgs e) { _onDone?.Invoke(null); Close(); }

    private void OnOk(object sender, RoutedEventArgs e)
    {
        var kind = KindBox.SelectedIndex == 1 ? NodeKind.Ssh : NodeKind.Local;
        var tags = TagsBox.Text
            .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            .ToList();

        MetricThresholds? custom = null;
        if (CustomThresholdsCheck.IsChecked == true)
        {
            double cpuW = CpuWarnSlider.Value / 100.0, cpuC = CpuCriticalSlider.Value / 100.0;
            double memW = MemWarnSlider.Value / 100.0, memC = MemCriticalSlider.Value / 100.0;
            double dW = DiskWarnSlider.Value / 100.0, dC = DiskCriticalSlider.Value / 100.0;
            custom = new MetricThresholds(
                Math.Min(cpuW, cpuC), Math.Max(cpuW, cpuC),
                Math.Min(memW, memC), Math.Max(memW, memC),
                Math.Min(dW, dC), Math.Max(dW, dC));
        }

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
            CustomThresholds = custom,
            Favorite = _existing?.Favorite ?? false,
            SnoozedUntil = _existing?.SnoozedUntil,
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
