import Foundation

enum Metric: String, Codable, CaseIterable, Hashable, Sendable {
    case cpu, mem, disk, net, health

    var displayName: String {
        switch self {
        case .cpu: return "CPU"
        case .mem: return "MEM"
        case .disk: return "DISK"
        case .net: return "NET"
        case .health: return "HEALTH"
        }
    }
}

struct FullViewContext: Codable, Hashable, Sendable {
    var hostId: UUID
    var metric: Metric
}
