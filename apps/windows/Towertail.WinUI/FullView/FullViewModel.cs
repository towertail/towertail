using CommunityToolkit.Mvvm.ComponentModel;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Transient state for the full-view window: zoom stack, pause mode, hover timestamp.
/// Kept off the shared <see cref="Towertail.WinUI.State.ServerViewModel"/> so closing the
/// full view discards its state automatically.
/// </summary>
public sealed partial class FullViewModel : ObservableObject
{
    [ObservableProperty] private bool _paused;
    [ObservableProperty] private DateTime? _hoverTimestamp;

    public Stack<(DateTime, DateTime)> ZoomStack { get; } = new();
}
