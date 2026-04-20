import SwiftUI

enum ThresholdTint: Sendable, Equatable {
    case nominal
    case warn
    case critical
    case stale

    var color: Color {
        switch self {
        case .nominal:  Color("Tint/Nominal")
        case .warn:     Color("Tint/Warn")
        case .critical: Color("Tint/Critical")
        case .stale:    Color.secondary
        }
    }
}

enum AggregateState: Sendable, Equatable {
    case nominal
    case warn
    case critical

    var tint: Color {
        switch self {
        case .nominal:  Color("Tint/Nominal")
        case .warn:     Color("Tint/Warn")
        case .critical: Color("Tint/Critical")
        }
    }
}
