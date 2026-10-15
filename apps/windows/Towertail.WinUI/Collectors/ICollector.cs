using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Abstraction that represents a running sampling loop for the full fleet. Production
/// wires <see cref="RealCollector"/>; tests and XAML previews wire <see cref="MockCollector"/>.
/// </summary>
public interface ICollector
{
    void Start();
    Task StopAsync();
    /// <summary>Trigger an immediate refresh for a single node (e.g., user-invoked "refresh now").</summary>
    Task RefreshAsync(Guid nodeId);
}
