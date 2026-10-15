using Microsoft.UI.Dispatching;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Bootstrap;

/// <summary>
/// Composition root. Wires the concrete stores, backend, collectors, and notifiers.
/// Mirrors AppEnvironment.swift on the Mac side.
/// </summary>
public sealed class AppEnvironment
{
    public required IBackend Backend { get; init; }
    public required ClientSettings ClientSettings { get; init; }
    public required ServerSettings ServerSettings { get; init; }
    public required NodeStore Nodes { get; init; }
    public required ServerStore Servers { get; init; }
    public required HistoryStore History { get; init; }
    public required ThresholdNotifier Notifier { get; init; }
    public required RealCollector Collector { get; init; }
    public required Logger Logger { get; init; }

    /// <summary>Optional TOFU prompter shared by collector + bulk-import Test path.</summary>
    public HostKeyPrompt? HostKeyPrompt { get; init; }
    public Action<Guid, string>? OnHostKeyTrust { get; init; }

    public static AppEnvironment Bootstrap()
    {
        TowertailApp.MainDispatcher = DispatcherQueue.GetForCurrentThread();
        var logger = Logger.Create();

        var settingsPath = TowertailApp.AppDataPath(TowertailApp.SettingsFileName);
        var historyPath = TowertailApp.AppDataPath(TowertailApp.HistoryFileName);

        var clientSettings = new ClientSettings(settingsPath);
        var serverSettings = new ServerSettings(settingsPath);
        var history = new HistoryStore(historyPath);
        var nodes = new NodeStore(settingsPath);
        var servers = new ServerStore(nodes, serverSettings, history);

        var backend = new LocalBackend(nodes, servers, clientSettings, serverSettings);
        var notifier = new ThresholdNotifier(serverSettings, nodes);
        servers.AttachNotifier(notifier);
        servers.AttachLogger(logger);

        // First-run seed: a Local node for "this machine" so the popover is not
        // empty on fresh installs. Users can rename, disable, or delete it.
        if (nodes.Nodes.Count == 0)
            nodes.Add(Node.LocalWindows(System.Environment.MachineName));

        // TOFU host-key wiring: the prompter shows a ContentDialog on the UI
        // dispatcher; the persister stamps the accepted fingerprint onto the
        // matching Node. Both are optional — nulls mean "refuse unknown keys".
        HostKeyPrompt? hostKeyPrompt = null;
        Action<Guid, string>? onTrust = null;
        if (TowertailApp.MainDispatcher is { } d)
        {
            hostKeyPrompt = new HostKeyTrustPrompter(d).Adapter;
            HostKeyTrustPersister.Bind(nodes);
            onTrust = HostKeyTrustPersister.Persist;
        }

        var invokerFactory = new SamplerInvokerFactory(hostKeyPrompt: hostKeyPrompt, onTrust: onTrust);
        var collector = new RealCollector(nodes, servers, serverSettings, invokerFactory, logger);
        // Marshal sample ingestion onto the WinUI dispatcher so observable
        // collections and PropertyChanged fire on the UI thread. Core doesn't
        // depend on WinUI, so we inject the dispatcher here.
        var dispatcher = TowertailApp.MainDispatcher;
        if (dispatcher != null)
        {
            collector.UiMarshaller = action =>
            {
                var tcs = new TaskCompletionSource();
                dispatcher.TryEnqueue(() =>
                {
                    try { action(); tcs.SetResult(); }
                    catch (Exception ex) { tcs.SetException(ex); }
                });
                return tcs.Task;
            };
        }
        collector.Start();

        return new AppEnvironment
        {
            Backend = backend,
            ClientSettings = clientSettings,
            ServerSettings = serverSettings,
            Nodes = nodes,
            Servers = servers,
            History = history,
            Notifier = notifier,
            Collector = collector,
            Logger = logger,
            HostKeyPrompt = hostKeyPrompt,
            OnHostKeyTrust = onTrust,
        };
    }
}
