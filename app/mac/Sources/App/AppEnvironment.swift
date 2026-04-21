import Foundation
import SwiftUI

@MainActor
final class AppEnvironment {
    let store: ServerStore
    let settings: AppSettings
    let nodeStore: NodeStore
    let history: HistoryStore
    let collector: any Collector
    private var task: Task<Void, Never>?

    init() {
        self.settings = AppSettings.loadFromDisk()
        self.nodeStore = NodeStore.loadFromDisk()
        self.history = HistoryStore(url: HistoryStore.defaultURL())
        self.store = ServerStore(history: history)
        self.collector = RealCollector(
            nodeStore: nodeStore,
            settings: settings,
            history: history
        )
    }

    func start() {
        guard task == nil else { return }
        let collector = self.collector
        let store = self.store
        task = Task.detached(priority: .utility) {
            await collector.run(sink: store)
        }
    }

    func stop() {
        task?.cancel()
        task = nil
    }
}
