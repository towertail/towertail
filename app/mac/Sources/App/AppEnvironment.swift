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
    let agentUpdater: AgentUpdateCoordinator
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
        let manifest = AgentManifestLoader.load()
        let updater = AgentUpdateCoordinator(manifest: manifest)
        self.agentUpdater = updater
        self.collector = RealCollector(
            nodeStore: nodeStore,
            settings: settings,
            history: history,
            agentUpdater: updater
        )
        let notifier = ThresholdNotifier(settings: settings)
        self.notifier = notifier
        store.notifier = notifier
    }

    func start() {
        guard task == nil else { return }
        let collector = self.collector
        let store = self.store
        notifier.start()
        task = Task.detached(priority: .utility) {
            await collector.run(sink: store)
        }
    }

    func stop() {
        task?.cancel()
        task = nil
    }
}
