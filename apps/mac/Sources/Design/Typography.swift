import SwiftUI

enum Typography {
    static let bigNumber = Font.system(size: 22, weight: .semibold, design: .rounded).monospacedDigit()
    static let netRate = Font.system(size: 15, weight: .semibold, design: .rounded).monospacedDigit()
    static let hostname = Font.system(size: 15, weight: .semibold)
    static let subtitle = Font.system(size: 11, weight: .regular)
    static let cellLabel = Font.system(size: 10, weight: .medium).monospacedDigit()
    static let headerTitle = Font.system(size: 13, weight: .semibold)
    static let metaText = Font.system(size: 11, weight: .regular).monospacedDigit()
    static let rowName = Font.system(size: 13, weight: .medium)
    static let rowValue = Font.system(size: 10.5, weight: .medium, design: .monospaced)
    static let rowTag = Font.system(size: 9, weight: .medium, design: .monospaced)
    static let rowColumn = Font.system(size: 9.5, weight: .medium, design: .monospaced)
}
