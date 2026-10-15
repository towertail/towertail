using System.ComponentModel;
using System.Runtime.CompilerServices;
using Microsoft.UI;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// Row VM for the Servers prefs list. Wraps a <see cref="Node"/> and pulls
/// live Status + Version off the matching <see cref="ServerViewModel"/> in
/// <see cref="ServerStore"/>, subscribing to property changes so the row
/// redraws when a new sample lands.
/// </summary>
public sealed class ServerListRow : INotifyPropertyChanged
{
    public event PropertyChangedEventHandler? PropertyChanged;

    public Node Node { get; private set; }
    private readonly ServerViewModel? _vm;

    public ServerListRow(Node node, ServerViewModel? vm)
    {
        Node = node;
        _vm = vm;
        if (_vm is not null)
        {
            _vm.PropertyChanged += OnVmChanged;
        }
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
        _ => Node.Kind.ToString(),
    };
    public string UserAtHost => Node.UserAtHost;
    public string EnabledText => Node.Enabled ? "True" : "False";

    public string StatusText
    {
        get
        {
            if (!Node.Enabled) return "disabled";
            if (_vm is null) return "—";
            if (_vm.OfflineReason is { Length: > 0 }) return "offline";
            if (_vm.LastSeen is null) return "…";
            return "online";
        }
    }

    public Brush StatusBrush => StatusText switch
    {
        "online" => new SolidColorBrush(Colors.SeaGreen),
        "offline" => new SolidColorBrush(Colors.IndianRed),
        "disabled" => new SolidColorBrush(Colors.Gray),
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
        Notify(nameof(EnabledText));
        Notify(nameof(StatusText));
        Notify(nameof(StatusBrush));
        Notify(nameof(VersionText));
    }
}
