using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Collectors;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Serializes first-connect host-key trust prompts on the UI dispatcher.
/// SSH.NET invokes the HostKeyReceived callback on a worker thread; we pump
/// back through the DispatcherQueue, show a <see cref="ContentDialog"/>, and
/// block the worker on the awaited result.
/// </summary>
public sealed class HostKeyTrustPrompter
{
    private readonly DispatcherQueue _dispatcher;
    private readonly SemaphoreSlim _gate = new(1, 1);

    public HostKeyTrustPrompter(DispatcherQueue dispatcher)
    {
        _dispatcher = dispatcher;
    }

    /// <summary>
    /// Returns the delegate to hand into <see cref="SamplerInvokerFactory"/>.
    /// </summary>
    public HostKeyPrompt Adapter => PromptAsync;

    private async Task<bool> PromptAsync(string host, string fingerprint)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            var tcs = new TaskCompletionSource<bool>();
            var queued = _dispatcher.TryEnqueue(async () =>
            {
                try { tcs.SetResult(await ShowDialogAsync(host, fingerprint)); }
                catch (Exception ex) { tcs.SetException(ex); }
            });
            if (!queued) return false;
            return await tcs.Task.ConfigureAwait(false);
        }
        finally { _gate.Release(); }
    }

    private static async Task<bool> ShowDialogAsync(string host, string fingerprint)
    {
        // ContentDialog needs a XamlRoot. Spin up an invisible host window so
        // the dialog doesn't depend on whether the tray popover happens to be
        // open at the moment the prompt fires.
        var host_window = new Window
        {
            SystemBackdrop = new Microsoft.UI.Xaml.Media.DesktopAcrylicBackdrop(),
        };
        host_window.AppWindow.IsShownInSwitchers = false;
        host_window.AppWindow.Title = "Trust host key?";
        var root = new Grid { Background = null };
        host_window.Content = root;
        host_window.Activate();

        try
        {
            var dialog = new ContentDialog
            {
                Title = $"Trust host {host}?",
                Content = $"First time connecting. The server presented key fingerprint:\n\n{fingerprint}\n\nTrust this key?",
                PrimaryButtonText = "Trust",
                CloseButtonText = "Cancel",
                DefaultButton = ContentDialogButton.Primary,
                XamlRoot = root.XamlRoot,
            };
            var result = await dialog.ShowAsync();
            return result == ContentDialogResult.Primary;
        }
        finally
        {
            host_window.Close();
        }
    }
}

/// <summary>
/// Bridges the TOFU accept path back to the live <see cref="State.NodeStore"/>.
/// Held as a static singleton because SamplerInvokerFactory lives in Core and
/// can't capture a specific instance across the assembly boundary.
/// </summary>
public static class HostKeyTrustPersister
{
    private static State.NodeStore? _store;

    public static void Bind(State.NodeStore store) => _store = store;

    public static void Persist(Guid nodeId, string fingerprint)
    {
        var store = _store;
        if (store is null) return;
        var existing = store.ById(nodeId);
        if (existing is null) return;
        store.Update(existing with { KnownHostFingerprint = fingerprint });
    }
}
