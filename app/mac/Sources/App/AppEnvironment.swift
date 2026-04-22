import Foundation
import SwiftUI

@MainActor
final class AppEnvironment {
    let store: ServerStore
    let settings: AppSettings
    let nodeStore: NodeStore
    let history: HistoryStore
    let collector: any Collector
    let notifier: ThresholdNotifier
    let samplerUpdater: SamplerUpdateCoordinator
    let reachability: SystemReachabilityMonitor
    private var task: Task<Void, Never>?

    init() {
        let settings = AppSettings.loadFromDisk()
        let nodeStore = NodeStore.loadFromDisk()
        let history = HistoryStore(url: HistoryStore.defaultURL())
        self.settings = settings
        self.nodeStore = nodeStore
        self.history = history
        let store = ServerStore(
            history: history,
            nodeLookup: { [weak nodeStore] id in nodeStore?.node(withId: id) }
        )
        self.store = store
        let manifest = SamplerManifestLoader.load()
        let updater = SamplerUpdateCoordinator(manifest: manifest)
        self.samplerUpdater = updater
        let reachability = SystemReachabilityMonitor(settings: settings)
        self.reachability = reachability
        self.collector = RealCollector(
            nodeStore: nodeStore,
            settings: settings,
            history: history,
            samplerUpdater: updater,
            reachability: reachability
        )
        let notifier = ThresholdNotifier(settings: settings, reachability: reachability)
        self.notifier = notifier
        store.notifier = notifier

        // Lifecycle breadcrumb — first thing that lands in today's log.
        let expected = manifest?.expectedSamplerField ?? "(none)"
        Logger.shared.info(
            "app: launch",
            category: "lifecycle",
            kv: [
                "nodes": String(nodeStore.nodes.count),
                "bundled_sampler": expected,
                "auto_update": String(settings.autoUpdateSamplersEnabled),
            ]
        )
    }

    func start() {
        guard task == nil else { return }
        Logger.shared.info("collector: starting", category: "lifecycle")
        let collector = self.collector
        let store = self.store
        reachability.start()
        notifier.start()
        task = Task.detached(priority: .utility) {
            await collector.run(sink: store)
        }
    }

    func stop() {
        Logger.shared.info("collector: stopping", category: "lifecycle")
        task?.cancel()
        task = nil
        reachability.stop()
    }
}
