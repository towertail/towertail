using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Per-window context passed down the XAML tree (the focused host, the current tab,
/// and the transient FullViewModel).
/// </summary>
public sealed record FullViewContext(ServerViewModel Host, FullViewModel Transient, string Tab);
