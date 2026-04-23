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

        var invokerFactory = new SamplerInvokerFactory();
        var collector = new RealCollector(nodes, servers, serverSettings, invokerFactory, logger);
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
        };
    }
}
