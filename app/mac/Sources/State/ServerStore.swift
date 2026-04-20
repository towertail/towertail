import Foundation
import SwiftUI

@Observable
@MainActor
final class ServerStore {
    private(set) var serverVMs: [ServerViewModel] = []

    func register(_ vm: ServerViewModel) {
        serverVMs.append(vm)
    }

    func ingest(_ sample: Sample, for id: UUID) {
        guard let vm = serverVMs.first(where: { $0.id == id }) else { return }
        vm.ingest(sample)
    }

    func markOffline(id: UUID, reason: String, at t: Date) {
        guard let vm = serverVMs.first(where: { $0.id == id }) else { return }
        vm.markOffline(reason: reason, at: t)
    }

    var aggregateState: AggregateState {
        var hasWarn = false
        var hasOffline = false
        for vm in serverVMs {
            switch vm.state {
            case .critical: return .critical
            case .warn: hasWarn = true
            case .offline: hasOffline = true
            default: break
            }
        }
        if hasWarn { return .warn }
        if hasOffline && serverVMs.allSatisfy({ $0.state.isOffline }) { return .critical }
        return .nominal
    }

    var summary: (online: Int, warn: Int, down: Int) {
        var online = 0, warn = 0, down = 0
        for vm in serverVMs {
            switch vm.state {
            case .online: online += 1
            case .warn: warn += 1; online += 1
            case .critical: warn += 1; online += 1
            case .offline: down += 1
            case .unknown: break
            }
        }
        return (online, warn, down)
    }
}
