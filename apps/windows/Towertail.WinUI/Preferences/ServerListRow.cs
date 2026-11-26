using System.ComponentModel;
using System.Runtime.CompilerServices;
using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// Row VM for the Servers prefs list. Wraps a <see cref="Node"/> and pulls
/// live Status + Version off the matching <see cref="ServerViewModel"/> in
/// <see cref="ServerStore"/>, subscribing to property changes so the row
/// redraws when a new sample lands.
/// </summary>
public sealed class ServerListRow : INotifyPropertyChanged, IDisposable
{
    public event PropertyChangedEventHandler? PropertyChanged;

    public Node Node { get; private set; }
    private readonly ServerViewModel? _vm;

    private readonly Func<Node, HostTag?>? _tagFor;
    private readonly Func<Node, HostStatus>? _statusFor;

    public ServerListRow(Node node, ServerViewModel? vm, Func<Node, HostTag?>? tagFor = null, Func<Node, HostStatus>? statusFor = null)
    {
        Node = node;
        _vm = vm;
        _tagFor = tagFor;
        _statusFor = statusFor;
        if (_vm is not null)
        {
            _vm.PropertyChanged += OnVmChanged;
        }
    }

    public void Dispose()
    {
        if (_vm is not null) _vm.PropertyChanged -= OnVmChanged;
    }

    public void UpdateNode(Node node)
    {
        Node = node;
        RaiseAll();
    }

    private void OnVmChanged(object? sender, PropertyChangedEventArgs e)
    {
        // Any VM change that might affect Status/Version — re-raise ours.
        if (e.PropertyName is nameof(ServerViewModel.LastSeen)
            or nameof(ServerViewModel.OfflineReason)
            or nameof(ServerViewModel.SamplerVersion))
        {
            Notify(nameof(StatusText));
            Notify(nameof(StatusBrush));
            Notify(nameof(VersionText));
        }
    }

    public string DisplayName => Node.DisplayName;
    public string KindLabel => Node.Kind switch
    {
        NodeKind.Local => "Local",
        NodeKind.Ssh => "SSH",
        NodeKind.Cloud => "Cloud",
        _ => Node.Kind.ToString(),
    };
    public string UserAtHost => Node.UserAtHost;

    private HostTag? Tag => _tagFor?.Invoke(Node);
    public string TagText => Tag?.Label() ?? "";
    public Visibility TagVisibility => Tag is null ? Visibility.Collapsed : Visibility.Visible;
    public Brush TagBrush => HostTagStyle.Brush(Tag ?? HostTag.SshOnly);

    /// <summary>The sign-in or the feed changed. The tag can differ.</summary>
    public void RefreshTag()
    {
        Notify(nameof(StatusText));
        Notify(nameof(StatusBrush));
        Notify(nameof(TagText));
        Notify(nameof(TagVisibility));
        Notify(nameof(TagBrush));
    }
    public string EnabledText => Node.Enabled ? "True" : "False";

    private HostStatus Status => _statusFor?.Invoke(Node) ?? HostStatus.For(Node, _vm, feedConnected: false, alerted: false);

    public string StatusText => Status.Text;

    public Brush StatusBrush => Status.Level switch
    {
        HostStatusLevel.Online => new SolidColorBrush(Colors.SeaGreen),
        HostStatusLevel.Offline => new SolidColorBrush(Colors.IndianRed),
        HostStatusLevel.Disabled => new SolidColorBrush(Colors.Gray),
        _ => new SolidColorBrush(Colors.Orange),
    };

    public string VersionText => _vm?.SamplerVersion ?? "—";

    private void Notify([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name ?? ""));

    private void RaiseAll()
    {
        Notify(nameof(DisplayName));
        Notify(nameof(KindLabel));
        Notify(nameof(UserAtHost));
        RefreshTag();
        Notify(nameof(EnabledText));
        Notify(nameof(StatusText));
        Notify(nameof(StatusBrush));
        Notify(nameof(VersionText));
    }
}
