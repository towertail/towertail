import SwiftUI

extension Color {
    static func threshold(_ value: Double, warn: Double, critical: Double) -> Color {
        if value >= critical { return ThresholdTint.critical.color }
        if value >= warn { return ThresholdTint.warn.color }
        return ThresholdTint.nominal.color
    }
}
