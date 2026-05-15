using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Shared kill-confirm-and-dispatch dialog flow used by both
/// <see cref="ProcessTable"/> and <see cref="PortsTable"/>. Lifted into a
/// helper so the two tables don't drift on phrasing or button shape.
///
/// The two-step dialog mirrors the Mac side: a confirmation
/// <see cref="ContentDialog"/> with the exact shell command preview, then
/// a result dialog (success or failure) so an SSH error stays visible.
/// </summary>
internal static class KillFlow
{
    public static async Task RunAsync(
        FrameworkElement host,
        Node node,
        int pid,
        string name,
        Action<bool> setInFlight)
    {
        var xamlRoot = host.XamlRoot;
        if (xamlRoot is null) return;

        var preview = ProcessKiller.CommandPreview(pid, node);
        var confirm = new ContentDialog
        {
            XamlRoot = xamlRoot,
            Title = $"Kill {name}?",
            Content = $"This will run:\n\n{preview}\n\nThe process will be terminated immediately.",
            PrimaryButtonText = $"Kill {pid}",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
        };
        // Mark the kill button red-ish via the destructive style. WinUI
        // doesn't have a per-button "destructive" flag, but coloring it
        // makes the action distinct enough to avoid accidental clicks.
        confirm.PrimaryButtonStyle = (Style?)Application.Current.Resources["AccentButtonStyle"];

        var result = await confirm.ShowAsync();
        if (result != ContentDialogResult.Primary) return;

        setInFlight(true);
        KillResult outcome;
        try
        {
            outcome = await ProcessKiller.KillAsync(pid, node);
        }
        finally
        {
            setInFlight(false);
        }

        var (title, body) = outcome switch
        {
            KillResult.Success => ("Process killed", $"Sent kill signal to {name} (PID {pid})."),
            KillResult.Failure f => ("Kill failed", f.Message),
            _ => ("Kill failed", "Unknown error"),
        };
        var done = new ContentDialog
        {
            XamlRoot = xamlRoot,
            Title = title,
            Content = body,
            CloseButtonText = "OK",
        };
        await done.ShowAsync();
    }
}
