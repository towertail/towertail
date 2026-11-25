using Microsoft.UI.Dispatching;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
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
    public required DiskScannerFactory DiskScanners { get; init; }
    public required DiskScanStore DiskScans { get; init; }
    public required CloudSession Cloud { get; init; }

    /// <summary>Watches sleep and wake for the cloud lease. Held here so that it lives as long as the app.</summary>
    public SystemReachabilityMonitor? Reachability { get; init; }

    /// <summary>Optional TOFU prompter shared by collector + bulk-import Test path.</summary>
    public HostKeyPrompt? HostKeyPrompt { get; init; }
    public Action<Guid, string>? OnHostKeyTrust { get; init; }

    public const string CloudFileName = "cloud.json";

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

        IBackend backend = new LocalBackend(nodes, servers, clientSettings, serverSettings);
#if DEBUG
        // Developer override: use a server as the backend, with no cloud account.
        var remoteMode = false;
        if (System.Environment.GetEnvironmentVariable("TOWERTAIL_REMOTE_URL") is { Length: > 0 } remoteUrl
            && System.Environment.GetEnvironmentVariable("TOWERTAIL_REMOTE_TOKEN") is { Length: > 0 } remoteToken
            && Uri.TryCreate(remoteUrl, UriKind.Absolute, out var remoteUri))
        {
            backend = new RemoteBackend(new RemoteClient(remoteUri, remoteToken), nodes, servers, clientSettings, serverSettings);
            remoteMode = true;
        }
#endif
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
        var updater = new SamplerUpdateCoordinator(
            SamplerManifest.LoadFromBundle(), new SshSamplerPusher(hostKeyPrompt, onTrust), logger);
        var collector = new RealCollector(nodes, servers, serverSettings, invokerFactory, logger, updater);
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

        var cloudPrefs = new CloudPrefs(TowertailApp.AppDataPath(CloudFileName), logger);
        var account = new CloudAccount(
            cloudPrefs.Endpoint,
            new DpapiTokenVault(TowertailApp.AppDataPath(DpapiTokenVault.FileName)),
            new CloudAuthClient(cloudPrefs.Endpoint),
            cloudPrefs, log: logger);
        var cloud = new CloudSession(account, nodes, servers, cloudPrefs, collector.UiMarshaller, logger);

        // The lease stops at sleep and comes back at wake.
        var reachability = new SystemReachabilityMonitor();
        reachability.AwakeChanged += cloud.Lease.SetSystemAwake;

        var startLocal = true;
#if DEBUG
        startLocal = !remoteMode;
        if (remoteMode) _ = ((RemoteBackend)backend).StartAsync();
#endif
        if (startLocal)
        {
            collector.Start();
            _ = cloud.StartAsync();
        }
        servers.StartStaleWatchdog(collector.UiMarshaller);
        _ = servers.HydrateAllProcsAsync(collector.UiMarshaller);

        // Disk scans of cloud hosts go through the cloud. The debug server override scans through the server.
        var remote = backend as RemoteBackend;
        var diskScanners = new DiskScannerFactory(
            hostKeyPrompt: hostKeyPrompt, onTrust: onTrust, updater: updater,
            remoteFor: n => n.IsCloud ? cloud.Client : remote?.Client,
            updates: (IDiskScanUpdates?)remote ?? cloud);
        var diskScans = new DiskScanStore(TowertailApp.AppDataPath(DiskScanStore.FileName));

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
            DiskScanners = diskScanners,
            DiskScans = diskScans,
            Cloud = cloud,
            Reachability = reachability,
            HostKeyPrompt = hostKeyPrompt,
            OnHostKeyTrust = onTrust,
        };
    }
}
