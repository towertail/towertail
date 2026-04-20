import Foundation
import SwiftUI

@MainActor
final class AppEnvironment {
    let store: ServerStore
    let settings: AppSettings
    let collector: any Collector
    private var task: Task<Void, Never>?

    init() {
        self.store = ServerStore()
        self.settings = AppSettings()
        self.collector = MockCollector()
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
