using Microsoft.UI.Xaml;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Keeps strong references to open FullViewWindow instances so WinUI doesn't
/// release them as soon as the opening method returns. Also focuses an
/// existing window for the same node rather than opening a duplicate.
/// </summary>
public static class FullViewRegistry
{
    private static readonly Dictionary<Guid, FullViewWindow> _open = new();

    public static FullViewWindow Open(ServerViewModel vm)
    {
        if (_open.TryGetValue(vm.Node.Id, out var existing))
        {
            existing.Activate();
            return existing;
        }
        var win = new FullViewWindow();
        win.Bind(vm);
        _open[vm.Node.Id] = win;
        win.Closed += (_, _) => _open.Remove(vm.Node.Id);
        win.Activate();
        return win;
    }

    public static bool IsAnyOpen => _open.Count > 0;
}
